module github.com/azthrun/dreambig-nas-files

go 1.27.2

// Why each dependency is here (CODING_STANDARDS.md: a dependency needs a reason):
require (
	// tusd: resumable Uploads over the tus protocol (file-api ADR-0003).
	github.com/tus/tusd/v2 v2.10.1
	// x/crypto: bcrypt for the Users' password hashes (ADR-0004); the standard library
	// has no bcrypt.
	golang.org/x/crypto v0.58.0
	// x/exp: tusd's handler.Config.Logger is an x/exp/slog Logger; used only to give it a
	// discarding logger.
	golang.org/x/exp v0.0.0-20260611194520-c48552f49976
	// x/sys: unix.Access to detect read-only Mounts and unix.PathMax; the standard
	// library's syscall has neither on macOS, where tests run.
	golang.org/x/sys v0.49.0
	// x/text: NFC normalisation of names (unicode/norm); the standard library has none,
	// and macOS clients send NFD names.
	golang.org/x/text v0.43.0
)
