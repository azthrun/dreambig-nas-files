#!/usr/bin/env bash
# Image smoke test: usage scripts/smoke-test.sh <image>
# Runs the built image as an arbitrary non-root uid with real bind mounts (each its own
# mount point) and checks the walking-skeleton behaviour. Needs docker and curl.
# Later tickets extend it with sign-in, Upload, download, delete and :ro write refusal.
set -euo pipefail

image="${1:?usage: smoke-test.sh <image>}"
uid=12345
gid=12345
name="nas-files-smoke-$$"
work="$(mktemp -d)"
rw="$work/docs"
ro="$work/archive"
mkdir -p "$rw" "$ro"
chmod 777 "$rw"
chmod 755 "$ro"
# Users file: one bcrypt entry, password generated per run (nothing secret in the repo).
smoke_pw="pw-$(date +%s)-$$"
docker run --rm httpd:2.4-alpine htpasswd -nbB smoke "$smoke_pw" > "$work/users.htpasswd"
chmod 644 "$work/users.htpasswd"
# Access file: smoke may write docs and archive (archive is :ro, so it caps at read).
echo '{"smoke":{"docs":"write","archive":"write"}}' > "$work/access.json"
chmod 644 "$work/access.json"
echo hello > "$ro/readme.txt"
chmod 644 "$ro/readme.txt"

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "--- container logs ---" >&2
    docker logs "$name" >&2 2>&1 || true
  fi
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker rm -f "$name-empty" "$name-bad" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# 1. The image itself defaults to a non-root user.
img_user="$(docker image inspect -f '{{.Config.User}}' "$image")"
case "$img_user" in "" | 0 | root | 0:* | root:*) fail "image runs as root (User=$img_user)" ;; esac

# 2. It starts as an arbitrary uid with one writable and one :ro Mount.
docker run -d --name "$name" --user "$uid:$gid" -p 127.0.0.1::8080 \
  -v "$rw:/data/docs" -v "$ro:/data/archive:ro" \
  -v "$work/users.htpasswd:/config/users.htpasswd:ro" \
  -v "$work/access.json:/config/access.json:ro" "$image" > /dev/null

# 3. It reports healthy (the image HEALTHCHECK runs the binary's -healthcheck).
health=""
for _ in $(seq 1 30); do
  running="$(docker inspect -f '{{.State.Running}}' "$name")"
  [ "$running" = true ] || fail "container exited"
  health="$(docker inspect -f '{{.State.Health.Status}}' "$name")"
  [ "$health" = healthy ] && break
  sleep 1
done
[ "$health" = healthy ] || fail "not healthy after 30s (status: $health)"

# 4. Actually running as the requested uid (distroless has no shell: read it from the host).
pid="$(docker inspect -f '{{.State.Pid}}' "$name")"
actual="$(ps -o uid=,gid= -p "$pid" | tr -s ' ' | sed 's/^ //')"
[ "$actual" = "$uid $gid" ] || fail "runs as '$actual', want '$uid $gid'"

# 5. /healthz answers over the published port without a session.
hostport="$(docker port "$name" 8080/tcp | head -n1 | sed 's/.*://')"
code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$hostport/healthz")"
[ "$code" = 200 ] || fail "/healthz answered $code"

# 6. The :ro Mount is recognised as an intentional read-only mount: no startup warning.
logs="$(docker logs "$name" 2>&1)"
if grep -q '"type":"warning"' <<< "$logs"; then
  fail "unexpected startup warning for a :ro Mount"
fi
grep -q '"mounts":\["archive","docs"\]' <<< "$logs" || fail "both Mounts not discovered"

# 6b. Sign-in: Root needs a session; the right password gets one and the Root lists Mounts.
base="http://127.0.0.1:$hostport"
code="$(curl -s -o /dev/null -w '%{http_code}' "$base/")"
[ "$code" = 303 ] || fail "Root without a session answered $code, want 303"
jar="$work/cookies"
code="$(curl -s -o /dev/null -w '%{http_code}' -c "$jar" --data-urlencode name=smoke --data-urlencode "password=$smoke_pw" "$base/login")"
[ "$code" = 303 ] || fail "sign-in answered $code, want 303"
curl -s -b "$jar" "$base/" | grep -q 'archive' || fail "signed-in Root does not list Mounts"
code="$(curl -s -o /dev/null -w '%{http_code}' --data-urlencode name=smoke --data-urlencode password=wrong "$base/login")"
[ "$code" = 401 ] || fail "failed sign-in answered $code, want 401"
docker logs "$name" 2>/dev/null | grep -q '"operation":"signin"' || fail "no audit line for sign-in"

# 6c. Access: a granted Mount browses; an Access file naming an unknown User is refused.
code="$(curl -s -o /dev/null -w '%{http_code}' -b "$jar" "$base/files/archive")"
[ "$code" = 200 ] || fail "browse archive answered $code, want 200"
echo '{"nobody":{"docs":"read"}}' > "$work/access-bad.json"
chmod 644 "$work/access-bad.json"
if docker run --name "$name-bad" --user "$uid:$gid" -v "$rw:/data/docs" \
  -v "$work/users.htpasswd:/config/users.htpasswd:ro" \
  -v "$work/access-bad.json:/config/access.json:ro" "$image" > /dev/null 2>&1; then
  fail "started with an Access file naming an unknown User"
fi
docker logs "$name-bad" 2>&1 | grep -q 'unknown User' || fail "no clear error for a bad Access file"

# 7. With no Mounts the container refuses to start, with a clear error.
if docker run --name "$name-empty" --user "$uid:$gid" "$image" > /dev/null 2>&1; then
  fail "started with no Mounts"
fi
docker logs "$name-empty" 2>&1 | grep -q 'no Mounts' || fail "no clear error for missing Mounts"

echo "smoke test passed"
