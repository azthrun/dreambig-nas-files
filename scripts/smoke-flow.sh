#!/usr/bin/env bash
# HTTP flow of the smoke test, against a running service:
#   scripts/smoke-flow.sh <base-url> <user> <password> <writable-mount> <ro-mount>
# Signs in (cookie jar + CSRF token scraped from the Root page), does one Upload (tus over
# curl), one download, one delete, then checks that <ro-mount> refuses every change.
# <ro-mount> must hold a file named readme.txt containing "hello". Needs curl and base64.
# smoke-test.sh runs it against the image; it also runs against a native binary.
set -euo pipefail

base="${1:?usage: smoke-flow.sh <base-url> <user> <password> <writable-mount> <ro-mount>}"
user="${2:?}"
pw="${3:?}"
rw="${4:?}"
ro="${5:?}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
jar="$tmp/cookies"

fail() { echo "FLOW FAIL: $*" >&2; exit 1; }

# status <expected> <what> <curl args...>: asserts the HTTP status; body and headers are
# left in $tmp/body and $tmp/headers.
status() {
  local want="$1" what="$2" got
  shift 2
  got="$(curl -s -o "$tmp/body" -D "$tmp/headers" -w '%{http_code}' "$@")"
  [ "$got" = "$want" ] || fail "$what answered $got, want $want"
}
# refused <what> <curl args...>: asserts a 4xx answer.
refused() {
  local what="$1" got
  shift
  got="$(curl -s -o "$tmp/body" -w '%{http_code}' "$@")"
  case "$got" in 4??) ;; *) fail "$what answered $got, want a 4xx refusal" ;; esac
}

# 1. Sign in. The session cookie goes in the jar; the CSRF token is in the Root page.
status 303 "sign-in" -c "$jar" --data-urlencode "name=$user" --data-urlencode "password=$pw" "$base/login"
status 200 "Root page" -b "$jar" "$base/"
csrf="$(sed -n 's/.*<meta name="csrf-token" content="\([^"]*\)".*/\1/p' "$tmp/body" | head -n1)"
[ -n "$csrf" ] || fail "no CSRF token on the Root page"

# A write without the token is refused even when signed in.
status 403 "write without a CSRF token" -b "$jar" --data-urlencode op=mkdir --data-urlencode name=nocsrf "$base/files/$rw"

# 2. One Upload over tus: create, then send all the bytes in one PATCH.
content="smoke-upload-$$"
size="${#content}"
meta="path $(printf %s "$rw/smoke-upload.txt" | base64 | tr -d '\n')"
status 201 "tus create" -b "$jar" -X POST "$base/uploads/" \
  -H "Tus-Resumable: 1.0.0" -H "X-CSRF-Token: $csrf" \
  -H "Upload-Length: $size" -H "Upload-Metadata: $meta"
loc="$(tr -d '\r' < "$tmp/headers" | sed -n 's/^[Ll]ocation: *//p' | head -n1)"
[ -n "$loc" ] || fail "tus create gave no Location"
loc="/uploads/${loc##*/uploads/}" # keep only the path: the host the server saw may differ
status 204 "tus PATCH" -b "$jar" -X PATCH "$base$loc" \
  -H "Tus-Resumable: 1.0.0" -H "X-CSRF-Token: $csrf" \
  -H "Upload-Offset: 0" -H "Content-Type: application/offset+octet-stream" --data-binary "$content"

# 3. Download it back, byte for byte, as an attachment.
status 200 "download" -b "$jar" "$base/files/$rw/smoke-upload.txt"
[ "$(cat "$tmp/body")" = "$content" ] || fail "downloaded content differs from the Upload"
grep -qi '^content-disposition: attachment' "$tmp/headers" || fail "download is not an attachment"

# 4. Delete it (to the Trash), then it is gone.
status 303 "delete" -b "$jar" --data-urlencode op=delete-confirm --data-urlencode "csrf_token=$csrf" \
  --data-urlencode names=smoke-upload.txt "$base/files/$rw"
status 404 "download after delete" -b "$jar" "$base/files/$rw/smoke-upload.txt"

# 5. The read-only Mount refuses every change and keeps its file.
refused "mkdir in the :ro Mount" -b "$jar" --data-urlencode op=mkdir --data-urlencode "csrf_token=$csrf" \
  --data-urlencode name=nope "$base/files/$ro"
refused "delete in the :ro Mount" -b "$jar" --data-urlencode op=delete-confirm --data-urlencode "csrf_token=$csrf" \
  --data-urlencode names=readme.txt "$base/files/$ro"
refused "tus create in the :ro Mount" -b "$jar" -X POST "$base/uploads/" \
  -H "Tus-Resumable: 1.0.0" -H "X-CSRF-Token: $csrf" -H "Upload-Length: 1" \
  -H "Upload-Metadata: path $(printf %s "$ro/nope.txt" | base64 | tr -d '\n')"
status 200 "download from the :ro Mount" -b "$jar" "$base/files/$ro/readme.txt"
[ "$(cat "$tmp/body")" = hello ] || fail "readme.txt in the :ro Mount changed"
status 404 "file that was refused" -b "$jar" "$base/files/$ro/nope.txt"

echo "flow passed"
