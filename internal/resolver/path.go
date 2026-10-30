// Package resolver is the Path resolver: the one validator every request-supplied
// location goes through before any os or filepath call (CODING_STANDARDS.md, Filesystem
// safety).
//
// The flow for every handler is:
//
//	p, err := resolver.ParseEscaped(escapedURLSuffix)   // syntax only; no filesystem
//	loc, err := res.Resolve(p)                          // real location, Mount, Hidden check
//	... judge Access by loc.Mount ...                   // never by the first segment sent
//	f, err := loc.Open()                                // I/O through the Mount's os.Root
//
// Errors wrap one of the sentinels below; Status maps them to HTTP in one place.
package resolver

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	// ErrInvalidPath means the request string breaks a syntax rule; it never reached
	// the filesystem.
	ErrInvalidPath = errors.New("invalid path")
	// ErrHidden means the Path names a Hidden Entry, directly or through a symlink.
	ErrHidden = errors.New("hidden entry")
	// ErrOutsideRoot means the Path resolves (through a symlink) outside every Mount.
	ErrOutsideRoot = errors.New("outside the root")
	// ErrNotFound means a valid Path names nothing, or names a Mount that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNotFolder means a Path that must be a folder is something else.
	ErrNotFolder = errors.New("not a folder")
)

// Status maps a resolver error to the HTTP status to send. Hidden Entries and escapes
// look exactly like not-found (404) so nothing about them can be probed.
func Status(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrInvalidPath):
		return http.StatusBadRequest
	case errors.Is(err, ErrHidden), errors.Is(err, ErrOutsideRoot), errors.Is(err, ErrNotFound), errors.Is(err, ErrNotFolder):
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// Path is a syntactically valid, NFC-normalised location relative to the Root: its first
// segment is a Mount name. The zero Path is the Root itself. A Path proves nothing about
// the filesystem; Resolver.Resolve does.
type Path struct {
	segs []string
}

// ParseEscaped validates the escaped (still percent-encoded) URL path suffix that names
// a Path, for example "docs/tax%20return" from /files/docs/tax%20return. Each segment is
// decoded on its own, so "%2F" or "%2e%2e" cannot smuggle a separator or a dot segment
// past the rules. "" is the Root.
func ParseEscaped(escaped string) (Path, error) {
	if escaped == "" {
		return Path{}, nil
	}
	if strings.HasPrefix(escaped, "/") {
		return Path{}, invalid("must be relative to the Root")
	}
	raw := strings.Split(escaped, "/")
	for i, seg := range raw {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return Path{}, invalid("bad percent-encoding")
		}
		raw[i] = dec
	}
	return ParseSegments(raw)
}

// Parse validates a plain (already decoded) slash-separated Path such as
// "documents/tax/2025.pdf"; "" is the Root.
func Parse(s string) (Path, error) {
	if s == "" {
		return Path{}, nil
	}
	if strings.HasPrefix(s, "/") {
		return Path{}, invalid("must be relative to the Root")
	}
	return ParseSegments(strings.Split(s, "/"))
}

// ParseSegments validates already-split, already-decoded segments. It rejects empty and
// dot segments, backslash, "/" inside a segment, control characters (which include
// NUL) and invalid UTF-8, and NFC-normalises each segment. Hidden Entry names are
// syntactically fine; Resolver.Resolve refuses them.
func ParseSegments(raw []string) (Path, error) {
	segs := make([]string, 0, len(raw))
	for _, seg := range raw {
		n, err := checkSegment(seg)
		if err != nil {
			return Path{}, err
		}
		segs = append(segs, n)
	}
	return Path{segs: segs}, nil
}

func checkSegment(seg string) (string, error) {
	switch {
	case seg == "":
		return "", invalid("empty path segment")
	case seg == "." || seg == "..":
		return "", invalid("dot segment")
	case strings.ContainsAny(seg, `/\`):
		return "", invalid("separator inside a segment")
	case strings.ContainsFunc(seg, unicode.IsControl):
		return "", invalid("control character")
	case !utf8.ValidString(seg):
		return "", invalid("not UTF-8")
	}
	return norm.NFC.String(seg), nil
}

// IsRoot reports whether p is the Root itself.
func (p Path) IsRoot() bool { return len(p.segs) == 0 }

// Segments returns a copy of p's segments.
func (p Path) Segments() []string { return append([]string{}, p.segs...) }

// Name is the last segment, or "" for the Root.
func (p Path) Name() string {
	if len(p.segs) == 0 {
		return ""
	}
	return p.segs[len(p.segs)-1]
}

// Parent is the Path one level up; the Root's parent is the Root.
func (p Path) Parent() Path {
	if len(p.segs) == 0 {
		return p
	}
	return Path{segs: p.segs[: len(p.segs)-1 : len(p.segs)-1]}
}

// Join returns p with name appended, validating name as one segment. Use it to turn a
// request-supplied file or folder name into a Path before resolving it.
func (p Path) Join(name string) (Path, error) {
	n, err := checkSegment(name)
	if err != nil {
		return Path{}, err
	}
	segs := make([]string, 0, len(p.segs)+1)
	segs = append(segs, p.segs...)
	return Path{segs: append(segs, n)}, nil
}

// String renders p as the Path the glossary defines: "documents/tax/2025.pdf", "" for
// the Root. It never contains an absolute NAS path; this is what audit lines record.
func (p Path) String() string { return strings.Join(p.segs, "/") }

// Escaped renders p as an escaped URL suffix, each segment percent-encoded.
func (p Path) Escaped() string {
	parts := make([]string, len(p.segs))
	for i, s := range p.segs {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

type pathError struct{ why string }

func (e *pathError) Error() string { return "invalid path: " + e.why }
func (e *pathError) Unwrap() error { return ErrInvalidPath }

func invalid(why string) error { return &pathError{why: why} }
