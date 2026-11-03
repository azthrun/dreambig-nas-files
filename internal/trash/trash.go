// Package trash is the Trash of each writable Mount (GLOSSARY.md): deleted and replaced
// content moves to <Mount>/.nas-files-trash/<YYYY-MM-DD>/<original path in the Mount>
// inside the same filesystem, never overwriting a Trash entry, and is purged after the
// retention period. A folder is renamed; a file is hard-linked into the Trash and then
// unlinked (rename would replace silently), falling back to a rename where the
// filesystem cannot hard-link. Either way the content never leaves the Mount.
//
// Place is the one operation that puts something in the Trash; the delete handler and
// "replace on Conflict" both use it. Move, for rename and move, lives here too: it is a
// tree change, so it needs the same write lock and the same never-replace rule.
package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/scrub"
)

// Config is what a Trash needs from outside.
type Config struct {
	// Mounts are the validated Mounts. Read-only Mounts have no Trash.
	Mounts []mounts.Mount
	// Clock dates the Trash folders and ages them for purging.
	Clock clock.Clock
	// RetentionDays is how many days Trash is kept; 0 means never purge.
	RetentionDays int
	// Audit receives the purge lines; nil discards them.
	Audit *audit.Logger
	// Log receives the causes of failed purges; nil discards them.
	Log *slog.Logger
	// RemoveAll deletes a purged folder inside a Mount's os.Root; nil means
	// root.RemoveAll. Tests substitute a slow one.
	RemoveAll func(root *os.Root, rel string) error
}

// Trash places content in the Trash and purges it. Safe for concurrent use.
type Trash struct {
	cfg   Config
	roots mounts.Roots
	// readOnly Mounts have no Trash.
	readOnly map[string]bool
	// mu is the process-wide write lock: every Trash placement and purge holds it.
	mu sync.Mutex
}

// New opens a Trash over the Mounts. Close it when done.
func New(cfg Config) (*Trash, error) {
	if cfg.RemoveAll == nil {
		cfg.RemoveAll = func(root *os.Root, rel string) error { return root.RemoveAll(rel) }
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	roots, err := mounts.OpenRoots(cfg.Mounts)
	if err != nil {
		return nil, err
	}
	t := &Trash{cfg: cfg, roots: roots, readOnly: map[string]bool{}}
	for _, m := range cfg.Mounts {
		t.readOnly[m.Name] = m.ReadOnly
	}
	return t, nil
}

// Close releases the Mounts' os.Roots.
func (t *Trash) Close() error { return t.roots.Close() }

// Place moves the file, folder or symlink at rel (slash-separated, inside the Mount) into
// the Mount's Trash and returns where it went, relative to the Mount. A symlink at rel
// is moved itself, never its target. Nothing in the Trash is ever overwritten: a name
// that is taken gets " (2)", " (3)", ... before the extension.
//
// The caller has already judged Access and read-only Mounts; Place refuses read-only
// Mounts again, the Mount's top level, and anything Hidden (so the Trash cannot be
// trashed into itself).
func (t *Trash) Place(mount, rel string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.place(mount, rel)
}

// Locked runs fn holding the process-wide write lock, so a caller can combine a
// placement with its own tree change (an Upload that replaces a file: Trash the old one,
// then link the new one) without another delete or placement slipping in between. place
// is Place without taking the lock again; fn must not call Place itself.
func (t *Trash) Locked(fn func(place func(mount, rel string) (string, error)) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return fn(t.place)
}

// Move renames the file, folder or symlink at from to to, both slash-separated and inside
// the one Mount, so it is atomic and never copies. A symlink is moved itself, never its
// target. Nothing is ever replaced: a taken name is ErrTaken. The one exception is a
// new spelling of the same entry on a case-insensitive filesystem (foo to Foo), where the
// name is "taken" by the entry itself.
//
// The caller has already judged Access; like Place, Move refuses read-only Mounts,
// the Mount's top level, anything Hidden, and a folder above from or to that became a link.
func (t *Trash) Move(mount, from, to string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	root, ok := t.roots[mount]
	if !ok {
		return ErrNoMount
	}
	if t.readOnly[mount] {
		return ErrReadOnly
	}
	if !validRel(from) || !validRel(to) || to == from || strings.HasPrefix(to, from+"/") {
		return ErrInvalid
	}
	if err := recheckParent(root, from); err != nil {
		return err
	}
	if err := recheckParent(root, to); err != nil {
		return err
	}
	fi, err := root.Lstat(from)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return moveError(err)
	}
	taken, err := root.Lstat(to)
	switch {
	case err == nil:
		if !sameEntry(from, to, fi, taken) {
			return ErrTaken
		}
		return moveError(root.Rename(from, to))
	case !errors.Is(err, fs.ErrNotExist):
		return moveError(err)
	case fi.IsDir():
		// Go's Rename refuses to move a folder onto an existing one, and the name was
		// just checked under the write lock.
		return moveError(root.Rename(from, to))
	}
	err = root.Link(from, to)
	switch {
	case err == nil:
		if err := root.Remove(from); err != nil {
			_ = root.Remove(to) // the file is still at from: leave one name, not two
			return moveError(err)
		}
		return nil
	case errors.Is(err, fs.ErrExist):
		return ErrTaken
	case noHardLinks(err):
		return moveError(root.Rename(from, to)) // the name was checked above, as in moveNoReplace
	}
	return moveError(err)
}

// sameEntry reports whether from and to are one entry spelled two ways.
func sameEntry(from, to string, fromInfo, toInfo fs.FileInfo) bool {
	return path.Dir(from) == path.Dir(to) && mounts.Fold(path.Base(from)) == mounts.Fold(path.Base(to)) && os.SameFile(fromInfo, toInfo)
}

// moveError maps a failed Move step to the sentinels, dropping OS paths.
func moveError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return ErrNotFound
	case errors.Is(err, syscall.ENAMETOOLONG):
		return resolver.ErrTooLong
	case errors.Is(err, syscall.EINVAL):
		return ErrInvalid // a folder into itself, reached by a spelling the check above missed
	}
	return fmt.Errorf("move: %w", scrub.Path(err))
}

// place is Place for a caller that holds t.mu. Right before the move it re-checks the
// parent folder of rel: the caller resolved it earlier, and a symlink may have taken its
// place since, so every folder above rel must still be a real folder, not a link.
func (t *Trash) place(mount, rel string) (string, error) {
	root, ok := t.roots[mount]
	if !ok {
		return "", ErrNoMount
	}
	if t.readOnly[mount] {
		return "", ErrReadOnly
	}
	if !validRel(rel) {
		return "", ErrInvalid
	}
	if err := recheckParent(root, rel); err != nil {
		return "", err
	}
	fi, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("trash: %w", scrub.Path(err))
	}
	dir, err := trashFolder(root, path.Join(TrashDir, t.cfg.Clock.Now().Format("2006-01-02")), path.Dir(rel))
	if err != nil {
		return "", fmt.Errorf("trash: %w", scrub.Path(err))
	}
	dest, err := moveNoReplace(root, rel, dir, path.Base(rel), fi.IsDir())
	if err != nil {
		return "", fmt.Errorf("trash: %w", scrub.Path(err))
	}
	return dest, nil
}

// recheckParent walks the folders above rel, one level at a time, and refuses the move
// (ErrInvalid) when one is a symlink or not a folder. A folder that has vanished is
// ErrNotFound. Paths from the resolver are already fully resolved, so a link here means
// the tree changed after the caller looked.
func recheckParent(root *os.Root, rel string) error {
	segs := strings.Split(rel, "/")
	for i := 1; i < len(segs); i++ {
		fi, err := root.Lstat(strings.Join(segs[:i], "/"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("trash: %w", scrub.Path(err))
		case !fi.IsDir():
			return ErrInvalid // a link, or a file where a folder was
		}
	}
	return nil
}

// validRel accepts a clean, relative, slash-separated path with no Hidden segment.
func validRel(rel string) bool {
	if rel == "" || rel == "." || path.Clean(rel) != rel || path.IsAbs(rel) {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || mounts.IsHiddenName(seg) {
			return false
		}
	}
	return true
}

// trashFolder makes sure base/<parent segments> exist inside the Trash and returns the
// folder to put the item in. A segment that is taken by something that is not a folder
// is replaced by a numbered sibling.
func trashFolder(root *os.Root, base, parent string) (string, error) {
	cur := TrashDir
	segs := strings.Split(strings.TrimPrefix(base, TrashDir+"/"), "/")
	if parent != "." {
		segs = append(segs, strings.Split(parent, "/")...)
	}
	if err := ensureDir(root, cur); err != nil {
		return "", err
	}
	for i, seg := range segs {
		name := seg
		if i < 1 { // the dated folder is fixed
			cur = path.Join(cur, name)
			if err := ensureDir(root, cur); err != nil {
				return "", err
			}
			continue
		}
		for n := 1; ; n++ {
			name = numbered(seg, n, true)
			fi, err := root.Lstat(path.Join(cur, name))
			if errors.Is(err, fs.ErrNotExist) {
				if err := root.Mkdir(path.Join(cur, name), 0o755); err != nil {
					return "", err
				}
				break
			}
			if err != nil {
				return "", err
			}
			if fi.IsDir() {
				break
			}
		}
		cur = path.Join(cur, name)
	}
	return cur, nil
}

func ensureDir(root *os.Root, rel string) error {
	fi, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return root.Mkdir(rel, 0o755)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", rel)
	}
	return nil
}

// numbered is name for n == 1, else the Numbered name "stem (n).ext": the first taken
// name gets (2), because the original counts as the first. (An Upload counts from 0:
// its first try is the requested name, so the two callers differ only in this offset;
// the naming rule itself is resolver.Numbered.)
func numbered(name string, n int, isDir bool) string {
	if n <= 1 {
		return name
	}
	return resolver.Numbered(name, n, isDir)
}

// moveNoReplace moves rel into dir under base (or a numbered variant) without ever
// replacing something there. rename(2) replaces silently, so a file is hard-linked into
// place (link fails with EEXIST) and then unlinked; a folder is renamed after checking
// that the name is free.
func moveNoReplace(root *os.Root, rel, dir, base string, isDir bool) (string, error) {
	for n := 1; ; n++ {
		dest := path.Join(dir, numbered(base, n, isDir))
		if isDir {
			// Go's Rename refuses to move a folder onto an existing one, and the write
			// lock keeps this process the only writer, so check the name is free.
			if _, err := root.Lstat(dest); err == nil {
				continue
			}
			return dest, root.Rename(rel, dest)
		}
		err := root.Link(rel, dest)
		switch {
		case err == nil:
			return dest, root.Remove(rel)
		case errors.Is(err, fs.ErrExist):
			continue
		case noHardLinks(err):
			// The filesystem cannot hard-link (the write lock still keeps us the only
			// writer in this process): check the name is free, then rename.
			if _, lerr := root.Lstat(dest); lerr == nil {
				continue
			}
			return dest, root.Rename(rel, dest)
		default:
			return "", err
		}
	}
}

func noHardLinks(err error) bool {
	for _, e := range []error{syscall.EXDEV, syscall.EPERM, syscall.ENOTSUP, syscall.ENOSYS, syscall.EOPNOTSUPP, syscall.EMLINK} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// TrashDir is the Trash folder name inside a Mount.
const TrashDir = mounts.TrashDir

// ErrNoMount means the named Mount is not known.
var ErrNoMount = errors.New("trash: unknown Mount")

var (
	// ErrReadOnly means the Mount is read-only and has no Trash.
	ErrReadOnly = errors.New("trash: Mount is read-only")
	// ErrInvalid means rel is not a place Place may move: empty, escaping, the Mount
	// itself, or Hidden.
	ErrInvalid = errors.New("trash: invalid path")
	// ErrNotFound means there is nothing at rel.
	ErrNotFound = errors.New("trash: nothing there")
	// ErrTaken means a Move's destination name is taken.
	ErrTaken = errors.New("trash: name taken")
)
