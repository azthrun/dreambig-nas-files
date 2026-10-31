# Coding standards

Go. Read `GLOSSARY.md` and `docs/adr/` first. Code from the sibling services is copied, never imported (ADR-0001).

## Layout

- `cmd/nas-files`: `main` only. Wires config, Mounts and the server; no logic.
- `internal/config`: the only package that reads the environment.
- `internal/mounts`: Mount discovery and validation, Hidden Entry names.
- `internal/server`: routes, middleware, handlers. `routes.go` is the one place routes are registered.
- `internal/clock`: the only source of time. Never call `time.Now()` elsewhere.
- `internal/testenv`: test support that builds the whole app around temp Mounts. Production code must not import it.
- New modules from the spec (path resolver, identity, access, listing, download, uploads, trash, audit, i18n) each get their own `internal/<name>` package.

## Filesystem safety

- One Path resolver validates every request-supplied string before any `os` or `filepath` call. Reject leading `/`, `.` and `..` segments, empty segments, backslash, control characters, invalid UTF-8 and NUL. Normalise each segment to NFC. Decode URL segments one by one so `%2F` and `%2e%2e` cannot smuggle.
- Judge Access by the real target's Mount after symlink resolution, never by the first segment as sent. Check Hidden Entries after resolution too.
- Hidden Entries (`@*`, `#*`, `.DS_Store`, Staging Area, `.nas-files-trash`, `.nas-mcp-trash`) are never listed and look like not-found. An inaccessible Mount behaves as nonexistent (404, not 403).
- Use `os.Root` (one per Mount) for I/O after validation.
- Never destroy data. Delete and replace move to the Mount's Trash by atomic same-filesystem rename, never overwriting a Trash entry. Only Staging Area data, Trash purge and zero-length temp files may be removed.
- Never overwrite silently. Complete Uploads with a create-exclusive hard link; `rename` would replace.
- The top level (Root and the Mounts themselves) is read-only. A read-only Mount refuses every change for every User.
- Serialise tree changes (Trash placement, purge, delete) with one process-wide `sync.Mutex`; re-check the resolved parent right before the rename.

## HTTP and security

- Only `/healthz`, sign-in, `/static` and `/lang/<code>` are unauthenticated: static assets and the language switch hold no file data, and the sign-in page needs both. Register every other route behind the session middleware. Every write, including tus requests, needs a CSRF token.
- Session cookie: HttpOnly, SameSite=Lax, not Secure (ADR-0002). Nothing may need a secure context.
- Compare secrets with `crypto/subtle`. Passwords are bcrypt only.
- Errors never echo absolute NAS paths or secrets. Map sentinel errors to HTTP statuses in one place.
- Every sign-in, write, purge and refusal emits one JSON audit line on stdout naming the User and Path. Operational logs go to stderr via `log/slog` JSON.

## Config

- Settings are `NAS_FILES_*` env vars, parsed only in `internal/config` via `Load(getenv)`; `main` passes `os.Getenv`. `Load` returns `Addr` even on error so `-healthcheck` works.
- A new variable goes into `config.EnvVars` and `.env.example` (a test checks both ways, including `compose.yaml`).
- Startup fails fast and clearly on a bad config, Mount, users file or Access file: log "refusing to start", exit 1. Errors never contain secrets or absolute NAS paths.
- Secrets come from env vars, mounted files or CI secrets, never the repo. The repo is public: docs and examples use placeholders (`<tailnet>`, `<user>`, `<share>`). Build token- or password-looking test values at runtime (for example `strings.Repeat`) so gitleaks stays quiet.

## Go

- Standard library first. Every other dependency gets a one-line reason comment next to it in `go.mod`.
- Wrap errors with `%w`. No panics for expected failures.
- Pass `context.Context` through request-scoped work. Every goroutine has a shutdown path.
- Wire dependencies through `server.Deps` and `server.New`. Inject the clock and anything environmental (free space) so tests can fake it.

## Tests

- Test through the highest seam: `httptest` against the full handler built by `testenv.New`, with temp folders as Mounts, test users and Access files, and the fake clock. Assert responses, files on disk and audit lines, never private helpers. HTML assertions stay on meaning (entries, links, controls), not markup.
- Packages are tested black-box (`package foo_test`).
- Each safety rule gets a test that tries to break it: path escape, symlink out of a Mount, symlink into a read-only Mount, Hidden Entry, no Access, read Access attempting a write, missing CSRF, missing session, a zip that would reach a Hidden Entry, Trash name clash, late Conflict.
- Temp folders share one filesystem, so tests run with the mount-point rule off. Case-clash tests skip on case-insensitive filesystems (macOS) and run on Linux CI.
- Browser Upload behaviour is checked by hand; there is no browser automation in v1.

## Deployment and CI

- Static binary (`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`) on `gcr.io/distroless/static-debian12:nonroot`, `linux/amd64` only. Compose overrides the user with the DSM UID:GID.
- Compose publishes exactly one host port and declares Mounts as volume lines.
- CI runs `go vet`, `go test`, a static build, gitleaks, compose validation, the image build and `scripts/smoke-test.sh`. Keep artifact retention short and upload only what is needed.
