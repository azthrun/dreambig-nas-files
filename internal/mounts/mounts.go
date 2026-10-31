// Package mounts discovers and validates the Mounts at startup (GLOSSARY.md, ADR-0003).
// Each folder directly under the data directory is a Mount, declared by a compose volume
// line. Any problem is a startup error so a wrong path never shows up as an empty folder.
package mounts

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/azthrun/dreambig-nas-files/internal/scrub"
)

// Mount is one validated Mount.
type Mount struct {
	// Name is the folder name; the first segment of every Path inside the Mount.
	Name string
	// Dir is the real OS path of the Mount (symlinks resolved). Never show it to Users.
	Dir string
	// ReadOnly is true when the service cannot write to the Mount. It refuses every
	// change from every User.
	ReadOnly bool
	// Warning is non-empty when the Mount is read-only for a reason the owner probably
	// did not intend (not mounted ":ro", just not writable by the service's user).
	Warning string
}

// Roots holds one os.Root per Mount, by Mount.Name.
type Roots map[string]*os.Root

// OpenRoots opens every Mount's os.Root. Close them when done.
func OpenRoots(ms []Mount) (Roots, error) {
	roots := Roots{}
	for _, m := range ms {
		root, err := os.OpenRoot(m.Dir)
		if err != nil {
			_ = roots.Close()
			return nil, fmt.Errorf("open Mount %q: %w", m.Name, scrub.Path(err))
		}
		roots[m.Name] = root
	}
	return roots, nil
}

// Close releases every os.Root.
func (r Roots) Close() error {
	var errs []error
	for _, root := range r {
		errs = append(errs, root.Close())
	}
	return errors.Join(errs...)
}

// Options controls Discover.
type Options struct {
	// RequireMountPoints refuses a folder that is not its own mount point. Keep it on
	// in production; temp folders in tests share a filesystem and need it off.
	RequireMountPoints bool
	// Link makes a hard link (new pointing at old); nil means os.Link. Tests substitute
	// one that fails, standing in for a filesystem without hard links.
	Link func(old, new string) error
}

// Discover validates every entry under root and returns the Mounts sorted by name.
func Discover(root string, opts Options) ([]Mount, error) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("data directory: %w", scrub.Path(err))
	}
	rootDev, ok := deviceOf(rootInfo)
	if !ok {
		return nil, errors.New("data directory: cannot read device id")
	}
	entries, err := os.ReadDir(root) // sorted by file name
	if err != nil {
		return nil, fmt.Errorf("data directory: %w", scrub.Path(err))
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })

	var out []Mount
	seen := map[string]string{} // folded name -> name
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("Mount %q: %w", name, scrub.Path(err))
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
			return nil, fmt.Errorf("Mount %q: %w", name, scrub.Path(err))
		}
		if err := unix.Access(real, unix.R_OK|unix.X_OK); err != nil {
			return nil, fmt.Errorf("Mount %q: not readable by the service's user; grant it Read access in DSM", name)
		}
		m := Mount{Name: name, Dir: real}
		if werr := unix.Access(real, unix.W_OK); werr != nil {
			m.ReadOnly = true
			if !errors.Is(werr, unix.EROFS) && !mountedReadOnly(real) {
				m.Warning = fmt.Sprintf("Mount %q is not writable by the service's user, so it is read-only; mount it :ro or grant the user Read/Write in DSM", name)
			}
		} else if warn, err := probeHardLinks(real, opts.Link); err != nil {
			return nil, fmt.Errorf("Mount %q: %w", name, err)
		} else if warn != "" {
			m.Warning = fmt.Sprintf("Mount %q: %s", name, warn)
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, errors.New("no Mounts: add at least one compose volume line mounting a shared folder under the data directory")
	}
	return out, nil
}

// probeFilePrefix names the probe's files. The Staging Area copy is Hidden by its
// folder; the link in the Mount's own folder starts with "@", a Hidden Entry name, so a
// crash can never leave a file Users see. Startup clears older ones.
const probeFilePrefix = "probe-"
const probeLinkPrefix = "@nas-files-probe-"

// noLink reports an error that says the filesystem cannot make hard links, as opposed to
// one that merely stopped the probe (full disk, denied create).
func noLink(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP)
}

// probeHardLinks checks that Uploads can complete in the writable Mount at dir: an
// Upload waits in the Staging Area and is hard-linked into place, so a filesystem that
// cannot link would fail every Upload after its data had travelled. It links a file from
// the Staging Area into the Mount's own folder and removes everything it made, including
// the Staging Area when it had to create it.
//
// Only a genuine no-link answer is an error. Any other trouble means the probe could not
// run, which says nothing about the filesystem: it comes back as a warning.
func probeHardLinks(dir string, link func(old, new string) error) (warning string, err error) {
	if link == nil {
		link = os.Link
	}
	unchecked := func(why string, cause error) string {
		if cause != nil {
			why += ": " + scrub.Path(cause).Error()
		}
		return "could not check that the filesystem supports hard links, which Uploads need (" + why + "); Uploads may fail"
	}
	staging := filepath.Join(dir, StagingDir)
	created := false
	if merr := os.Mkdir(staging, 0o700); merr == nil {
		created = true
	} else if !errors.Is(merr, os.ErrExist) {
		return unchecked("cannot create its Staging Area", merr), nil
	}
	if fi, lerr := os.Lstat(staging); lerr != nil || !fi.IsDir() {
		return unchecked("its Staging Area is not a folder; remove it from the Mount", nil), nil
	}
	clearStaleProbes(dir, staging)
	var tag [8]byte
	if _, rerr := rand.Read(tag[:]); rerr != nil {
		return unchecked("no random name", rerr), nil
	}
	src := filepath.Join(staging, probeFilePrefix+hex.EncodeToString(tag[:]))
	dst := filepath.Join(dir, probeLinkPrefix+hex.EncodeToString(tag[:]))
	defer func() {
		_ = os.Remove(dst)
		_ = os.Remove(src)
		if created {
			_ = os.Remove(staging) // only if empty, which it is unless somebody else just used it
		}
	}()
	if werr := os.WriteFile(src, nil, 0o600); werr != nil {
		return unchecked("cannot write to its Staging Area", werr), nil
	}
	if lerr := link(src, dst); lerr != nil {
		if noLink(lerr) {
			return "", fmt.Errorf("the filesystem does not support hard links, which Uploads use to complete without overwriting a file: %w", scrub.Path(lerr))
		}
		return unchecked("the probe link failed", lerr), nil
	}
	return "", nil
}

// clearStaleProbes removes probe files an earlier run left behind by crashing.
func clearStaleProbes(dir, staging string) {
	for _, d := range []struct{ dir, prefix string }{{dir, probeLinkPrefix}, {staging, probeFilePrefix}} {
		ents, err := os.ReadDir(d.dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), d.prefix) && e.Type().IsRegular() {
				_ = os.Remove(filepath.Join(d.dir, e.Name()))
			}
		}
	}
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
