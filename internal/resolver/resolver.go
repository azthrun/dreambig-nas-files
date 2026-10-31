package resolver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
)

// Resolver turns Paths into real locations. It holds one os.Root per Mount, because
// Mounts are separate filesystems; all I/O on a Location goes through the os.Root of
// the Mount it really lives in, so a symlink swapped in after resolution still cannot
// escape that Mount.
type Resolver struct {
	mounts []mounts.Mount
	roots  map[string]*os.Root // by Mount.Name
}

// New opens a Resolver over the Mounts from mounts.Discover. Close it when done.
func New(ms []mounts.Mount) (*Resolver, error) {
	r := &Resolver{mounts: ms, roots: map[string]*os.Root{}}
	for _, m := range ms {
		root, err := os.OpenRoot(m.Path)
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("open Mount %q: %w", m.Name, err)
		}
		r.roots[m.Name] = root
	}
	return r, nil
}

// Close releases every Mount's os.Root.
func (r *Resolver) Close() error {
	var errs []error
	for _, root := range r.roots {
		errs = append(errs, root.Close())
	}
	return errors.Join(errs...)
}

// Location is a validated Path resolved against the filesystem. Hand it to code that
// does I/O; never build an OS path from a request string any other way.
type Location struct {
	// Path is the Path as requested (what to show, link and audit).
	Path Path
	// Mount is the Mount the real target lives in, after resolving symlinks. Judge
	// Access by it, never by Path's first segment. Zero for the Root.
	Mount mounts.Mount
	// Real is the target's location inside Mount after resolving symlinks, as
	// segments; empty for the Mount folder itself. Valid whether or not the target
	// exists: for a missing target it is the real parent plus the missing names.
	Real []string
	// Exists is false for a Path that names nothing yet (a place to create).
	Exists bool

	isRoot bool
	res    *Resolver
}

// IsRoot reports whether the Location is the Root, which has no Mount.
func (l Location) IsRoot() bool { return l.isRoot }

// Rel is the target's slash-separated location inside Mount ("." for the Mount folder).
// It is relative to the Mount's os.Root, see OS.
func (l Location) Rel() string {
	if len(l.Real) == 0 {
		return "."
	}
	return strings.Join(l.Real, "/")
}

// OS returns the Mount's os.Root. Later tickets that create, rename or remove must use
// it with Rel (after judging Access by Mount). Do not Close it.
func (l Location) OS() (*os.Root, error) {
	if l.isRoot {
		return nil, ErrNotFolder
	}
	return l.res.roots[l.Mount.Name], nil
}

// Resolve checks p against the filesystem: the first segment must be a Mount; symlinks
// in the part that exists are resolved; the result must still be inside a Mount (any
// Mount, which then becomes Location.Mount) and name no Hidden Entry. A target that
// does not exist yet is allowed (Exists false) so create and Upload can use it, but a
// dangling or escaping symlink in its chain is never written through.
func (r *Resolver) Resolve(p Path) (Location, error) {
	if p.IsRoot() {
		return Location{Path: p, isRoot: true, Exists: true, res: r}, nil
	}
	for _, seg := range p.segs {
		if mounts.IsHiddenName(seg) {
			return Location{}, ErrHidden
		}
	}
	var mount *mounts.Mount
	for i := range r.mounts {
		if norm.NFC.String(r.mounts[i].Name) == p.segs[0] {
			mount = &r.mounts[i]
			break
		}
	}
	if mount == nil {
		return Location{}, ErrNotFound
	}
	loc, err := r.locate(*mount, r.diskNames(*mount, p.segs[1:]))
	if err != nil {
		return Location{}, err
	}
	loc.Path = p
	return loc, nil
}

// diskNames maps NFC request segments to the names stored on disk. Linux filesystems
// keep whatever bytes the client sent, so a file saved from a Mac may be stored NFD
// while its link (built from the NFC name we show) is NFC. For each segment: the exact
// name wins; otherwise the folder entry whose name normalises to the segment is used.
// Segments that match nothing are kept as requested (a missing target). Only names are
// read here; locate still resolves, bounds and Hidden-checks the result.
func (r *Resolver) diskNames(mount mounts.Mount, segs []string) []string {
	out := make([]string, 0, len(segs))
	dir := mount.Path
	for i, seg := range segs {
		name := seg
		if !isASCII(seg) {
			if _, err := os.Lstat(filepath.Join(dir, seg)); err != nil {
				if got, ok := normalisedMatch(dir, seg); ok {
					name = got
				}
			}
		}
		out = append(out, name)
		dir = filepath.Join(dir, name)
		if _, err := os.Stat(dir); err != nil { // nothing deeper can exist
			return append(out, segs[i+1:]...)
		}
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// normalisedMatch finds the entry of dir whose NFC form is want.
func normalisedMatch(dir, want string) (string, bool) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, de := range des {
		if norm.NFC.String(de.Name()) == want {
			return de.Name(), true
		}
	}
	return "", false
}

// locate resolves segs below mount's folder.
func (r *Resolver) locate(mount mounts.Mount, segs []string) (Location, error) {
	n := len(segs)
	var real string
	for ; ; n-- {
		abs := filepath.Join(append([]string{mount.Path}, segs[:n]...)...)
		got, err := filepath.EvalSymlinks(abs)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			if n == 0 {
				return Location{}, ErrNotFound // the Mount folder itself vanished
			}
			continue // missing, or a file stands where a folder should
		}
		if err != nil && isLoop(err) {
			return Location{}, ErrNotFound // a symlink loop leads nowhere
		}
		if err != nil {
			return Location{}, fmt.Errorf("resolve path: %w", scrub(err))
		}
		real = got
		break
	}
	owner, rel, ok := r.ownerOf(real)
	if !ok {
		return Location{}, ErrOutsideRoot
	}
	for _, s := range rel {
		if mounts.IsHiddenName(s) {
			return Location{}, ErrHidden
		}
	}
	return Location{
		Mount:  owner,
		Real:   append(rel, segs[n:]...),
		Exists: n == len(segs),
		res:    r,
	}, nil
}

// isLoop reports a symlink loop. filepath.EvalSymlinks gives up on one with a plain
// error ("too many links") that carries no errno, so the text is all there is to match.
func isLoop(err error) bool {
	return errors.Is(err, syscall.ELOOP) || strings.Contains(err.Error(), "too many links")
}

// ownerOf finds the Mount whose folder contains the real absolute path (the deepest one
// if Mounts nest) and the path's segments inside it.
func (r *Resolver) ownerOf(real string) (mounts.Mount, []string, bool) {
	var best *mounts.Mount
	for i := range r.mounts {
		m := &r.mounts[i]
		if real == m.Path || strings.HasPrefix(real, m.Path+string(filepath.Separator)) {
			if best == nil || len(m.Path) > len(best.Path) {
				best = m
			}
		}
	}
	if best == nil {
		return mounts.Mount{}, nil, false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(real, best.Path), string(filepath.Separator))
	if rest == "" {
		return *best, nil, true
	}
	return *best, strings.Split(rest, string(filepath.Separator)), true
}

// scrub drops the OS path from a *PathError so errors never carry absolute NAS paths.
func scrub(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// Stat describes the target, following symlinks. ErrNotFound when it does not exist.
func (l Location) Stat() (fs.FileInfo, error) {
	if l.isRoot {
		return nil, ErrNotFolder
	}
	if !l.Exists {
		return nil, ErrNotFound
	}
	root, _ := l.OS()
	fi, err := root.Stat(l.Rel())
	if err != nil {
		return nil, mapIO(err)
	}
	return fi, nil
}

// Open opens the target for reading through the Mount's os.Root.
func (l Location) Open() (*os.File, error) {
	if l.isRoot {
		return nil, ErrNotFolder
	}
	if !l.Exists {
		return nil, ErrNotFound
	}
	root, _ := l.OS()
	f, err := root.Open(l.Rel())
	if err != nil {
		return nil, mapIO(err)
	}
	return f, nil
}

func mapIO(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) {
		return ErrNotFound
	}
	// os.Root refuses a path that escapes its Mount with a plain error.
	if strings.Contains(err.Error(), "escapes") {
		return ErrOutsideRoot
	}
	return fmt.Errorf("io: %w", scrub(err))
}

// Entry is one visible item in a folder.
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
	// Mount is the name of the Mount the entry really lives in: a symlink into another
	// Mount reports that Mount, so callers can drop entries the User has no Access to.
	Mount string
}

// ReadDir lists the folder, unsorted. Hidden Entries are never included; neither are
// symlinks that are broken, escape every Mount or lead to a Hidden Entry. Symlinks
// that stay inside the Root are listed as what they point at.
func (l Location) ReadDir() ([]Entry, error) {
	f, err := l.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.IsDir() {
		return nil, ErrNotFolder
	}
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read folder: %w", scrub(err))
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if mounts.IsHiddenName(name) {
			continue
		}
		var fi fs.FileInfo
		mount := l.Mount.Name
		if de.Type()&fs.ModeSymlink != 0 {
			fi, mount = l.followChild(name)
		} else if got, err := de.Info(); err == nil {
			fi = got
		}
		if fi == nil {
			continue // removed meanwhile, or a link we refuse to show
		}
		e := Entry{Name: norm.NFC.String(name), IsDir: fi.IsDir(), ModTime: fi.ModTime(), Mount: mount}
		if !fi.IsDir() {
			e.Size = fi.Size()
		}
		out = append(out, e)
	}
	return out, nil
}

// followChild stats the symlink name in this folder as the Resolver would resolve it,
// or returns nil when it must not be shown.
func (l Location) followChild(name string) (fs.FileInfo, string) {
	child, err := l.res.locate(l.Mount, append(append([]string{}, l.Real...), name))
	if err != nil || !child.Exists {
		return nil, ""
	}
	fi, err := child.Stat()
	if err != nil {
		return nil, ""
	}
	return fi, child.Mount.Name
}
