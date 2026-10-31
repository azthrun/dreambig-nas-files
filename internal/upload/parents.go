package upload

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"

	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/scrub"
)

// parentConflict is a file (or link) standing where a parent folder is needed. It is a
// Conflict (409) but no choice can settle it, so the Upload's data is not kept for resolve.
type parentConflict struct{}

func (parentConflict) Error() string        { return ErrConflict.Error() + " where a folder is needed" }
func (parentConflict) Is(target error) bool { return target == ErrConflict }

var errParentConflict error = parentConflict{}

// checkParents is the Folder Upload rule that replaced "the parent must exist": every
// folder of p that does not exist yet will be created at completion, so its name must
// pass the write-side name rules (ValidateName, which also refuses Hidden Entry names);
// the deepest folder that does exist must really be a folder, else a file stands where
// a folder is needed (ErrConflict). Existing folders were named by someone else and are
// not re-judged by name. Nothing is created here.
func (h *Handler) checkParents(p resolver.Path) error {
	for anc := p.Parent(); ; anc = anc.Parent() {
		if anc.IsRoot() {
			return resolver.ErrNotFound // the Root has no files, only Mounts
		}
		loc, err := h.Resolver.Resolve(anc)
		if err != nil {
			return err
		}
		if loc.Exists {
			if fi, err := loc.Stat(); err != nil || !fi.IsDir() {
				return errParentConflict
			}
			return nil
		}
		if root, err := loc.OS(); err == nil {
			if fi, err := root.Lstat(loc.Rel()); err == nil {
				if fi.IsDir() {
					return nil // another Upload just created it
				}
				return errParentConflict // a dangling link stands there; it is never written through
			}
		}
		if _, err := resolver.ValidateName(anc.Name()); err != nil {
			return err
		}
	}
}

// makeParents creates the folders that loc's real parent chain is missing, in the Mount's
// os.Root, one level at a time with Mkdir. It runs only when an Upload completes. Mkdir
// is atomic, so two Uploads creating the same folder both succeed (EEXIST is fine if
// what stands there is a real folder). It reports whether it had to create anything.
// A symlink or file found in the chain, even one swapped in meanwhile, is a Conflict:
// created parents never follow a link, and os.Root keeps every step inside the Mount.
func makeParents(loc resolver.Location) (bool, error) {
	root, err := loc.OS()
	if err != nil {
		return false, err
	}
	created := false
	for i := 1; i < len(loc.Real); i++ {
		dir := strings.Join(loc.Real[:i], "/")
		fi, err := root.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			err = root.Mkdir(dir, 0o755)
			if err == nil {
				created = true
			} else if !errors.Is(err, fs.ErrExist) {
				return created, mapMkdir(err)
			}
			fi, err = root.Lstat(dir)
		}
		if err != nil {
			return created, mapMkdir(err)
		}
		if !fi.IsDir() { // a file, or a link (never followed)
			return created, errParentConflict
		}
	}
	return created, nil
}

func mapMkdir(err error) error {
	switch {
	case errors.Is(err, syscall.ENOTDIR):
		return errParentConflict
	case errors.Is(err, fs.ErrNotExist):
		return resolver.ErrNotFound
	}
	if resolver.IsEscape(err) {
		return resolver.ErrOutsideRoot
	}
	return fmt.Errorf("create parent folder: %w", scrub.Path(err))
}
