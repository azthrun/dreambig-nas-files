package resolver

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
)

// ErrInvalidName means a name a User wants to create (folder, Upload, rename target)
// breaks the write-side rules. It is also an ErrInvalidPath (400).
var ErrInvalidName = errors.New("invalid name")

// maxNameBytes is the longest single name Synology's filesystems accept.
const maxNameBytes = 255

// windowsReserved are device names Windows clients cannot open, with or without an
// extension. A NAS shared over SMB must not create them.
var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateName is THE write-side name rule: every ticket that creates a name from
// request input (create folder, Upload, Folder Upload) must pass it through here, or
// through Path.JoinNew, which calls it. It applies the read-side segment rules (no
// separators, dot segments, control characters or invalid UTF-8) plus: none of
// \ : * ? " < > |, no trailing "." or space, no Windows device names, no Hidden Entry
// names, at most 255 bytes. It returns the NFC-normalised name.
func ValidateName(name string) (string, error) {
	n, err := checkSegment(name)
	if err != nil {
		return "", badName(err.Error())
	}
	switch {
	case strings.ContainsAny(n, `\:*?"<>|`):
		return "", badName("forbidden character")
	case strings.HasSuffix(n, ".") || strings.HasSuffix(n, " "):
		return "", badName("trailing dot or space")
	case len(n) > maxNameBytes:
		return "", badName("too long")
	case windowsReserved[strings.ToLower(stem(n))]:
		return "", badName("reserved name")
	case mounts.IsHiddenName(n):
		return "", badName("reserved name")
	}
	return n, nil
}

// stem is name up to its first dot ("nul.tar.gz" -> "nul"), which is how Windows
// decides a device name.
func stem(name string) string {
	stem, _, _ := strings.Cut(name, ".")
	return strings.TrimRight(stem, " ")
}

// Numbered is the name for copy number n: name itself for n < 1, else "stem (n).ext"
// with the number before the extension. Folders and dotfiles (".config") have no
// extension, so the number goes at the end. It is the one rule for "keep both" Uploads
// and for Trash entries whose name is taken.
func Numbered(name string, n int, isDir bool) string {
	if n < 1 {
		return name
	}
	ext := ""
	if !isDir {
		ext = path.Ext(name)
		if ext == name { // a dotfile has no extension
			ext = ""
		}
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
}

// JoinNew is Join for a name that will be created: it applies ValidateName.
func (p Path) JoinNew(name string) (Path, error) {
	n, err := ValidateName(name)
	if err != nil {
		return Path{}, err
	}
	return p.Join(n)
}

type nameError struct{ why string }

func (e *nameError) Error() string   { return "invalid name: " + e.why }
func (e *nameError) Unwrap() []error { return []error{ErrInvalidName, ErrInvalidPath} }

func badName(why string) error { return &nameError{why: why} }
