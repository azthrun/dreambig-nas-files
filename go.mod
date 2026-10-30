module github.com/azthrun/dreambig-nas-files

go 1.27.2

// Why each dependency is here (CODING_STANDARDS.md: a dependency needs a reason):
require (
	// x/crypto: bcrypt for the Users' password hashes (ADR-0004); the standard library
	// has no bcrypt.
	golang.org/x/crypto v0.58.0
	// x/sys: unix.Access to detect read-only Mounts; the standard library's syscall has
	// no Access on macOS, where tests run.
	golang.org/x/sys v0.49.0
	// x/text: NFC normalisation of names (unicode/norm); the standard library has none,
	// and macOS clients send NFD names.
	golang.org/x/text v0.43.0
)
