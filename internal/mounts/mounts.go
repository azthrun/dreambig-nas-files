// Package mounts discovers and validates the Mounts at startup (GLOSSARY.md, ADR-0003).
// Each folder directly under the data directory is a Mount, declared by a compose volume
// line. Any problem is a startup error so a wrong path never shows up as an empty folder.
package mounts

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Mount is one validated Mount.
type Mount struct {
	// Name is the folder name; the first segment of every Path inside the Mount.
	Name string
	// Path is the real OS path of the Mount (symlinks resolved). Never show it to Users.
	Path string
	// ReadOnly is true when the service cannot write to the Mount. It refuses every
	// change from every User.
	ReadOnly bool
	// Warning is non-empty when the Mount is read-only for a reason the owner probably
	// did not intend (not mounted ":ro", just not writable by the service's user).
	Warning string
}

// Options controls Discover.
type Options struct {
	// RequireMountPoints refuses a folder that is not its own mount point. Keep it on
	// in production; temp folders in tests share a filesystem and need it off.
	RequireMountPoints bool
}

// Discover validates every entry under root and returns the Mounts sorted by name.
func Discover(root string, opts Options) ([]Mount, error) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("data directory: %w", scrub(err))
	}
	rootDev, ok := deviceOf(rootInfo)
	if !ok {
		return nil, errors.New("data directory: cannot read device id")
	}
	entries, err := os.ReadDir(root) // sorted by file name
	if err != nil {
		return nil, fmt.Errorf("data directory: %w", scrub(err))
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })

	var out []Mount
	seen := map[string]string{} // folded name -> name
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("Mount %q: %w", name, scrub(err))
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("Mount %q: not a folder; every entry under the data directory must be a Mount declared by a compose volume line", name)
		}
		// Checked before the name rules so an old single-folder layout gets this hint.
		if dev, ok := deviceOf(info); opts.RequireMountPoints && (!ok || dev == rootDev) {
			return nil, fmt.Errorf("Mount %q: not its own mount point; declare it as its own compose volume line", name)
		}
		if isReservedMountName(name) {
			return nil, fmt.Errorf("Mount %q: reserved or hidden name; rename the volume mount", name)
		}
		if prev, dup := seen[Fold(name)]; dup {
			return nil, fmt.Errorf("Mounts %q and %q differ only in case or Unicode form; rename one", prev, name)
		}
		seen[Fold(name)] = name

		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("Mount %q: %w", name, scrub(err))
		}
		if err := unix.Access(real, unix.R_OK|unix.X_OK); err != nil {
			return nil, fmt.Errorf("Mount %q: not readable by the service's user; grant it Read access in DSM", name)
		}
		m := Mount{Name: name, Path: real}
		if werr := unix.Access(real, unix.W_OK); werr != nil {
			m.ReadOnly = true
			if !errors.Is(werr, unix.EROFS) && !mountedReadOnly(real) {
				m.Warning = fmt.Sprintf("Mount %q is not writable by the service's user, so it is read-only; mount it :ro or grant the user Read/Write in DSM", name)
			}
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, errors.New("no Mounts: add at least one compose volume line mounting a shared folder under the data directory")
	}
	return out, nil
}

// scrub drops the OS path from a *PathError so startup errors name only what the owner
// configured.
func scrub(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func deviceOf(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // Dev is int32 on darwin
}

// mountedReadOnly reports whether path is a mount point mounted "ro", from
// /proc/self/mountinfo. False when the file is unreadable (non-Linux).
func mountedReadOnly(path string) bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 || unescapeMountinfo(fields[4]) != path {
			continue
		}
		return slices.Contains(strings.Split(fields[5], ","), "ro")
	}
	return false
}

// unescapeMountinfo decodes the octal escapes (\040 for a space) mountinfo uses.
func unescapeMountinfo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
