package upload

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"syscall"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/scrub"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
)

// ErrConflict means the Path already holds something (or a file stands where a folder
// should be). It is how a placement is refused; the User gets 409.
var ErrConflict = errors.New("a file already exists at that Path")

// Placement is one completed Upload waiting to be put at its Path.
type Placement struct {
	// User is who uploaded it.
	User string
	// Dest is the destination, freshly resolved and already authorised for writing:
	// Dest.Mount is its real Mount, Dest.Rel() the location inside it. Exists is false
	// unless the Path was taken meanwhile.
	Dest resolver.Location
	// Staged is the Upload's data inside Dest.Mount's os.Root (".nas-files-staging/<id>").
	Staged string
	// Choice is the User's Conflict choice from the Upload's metadata: "", ChoiceKeep or
	// ChoiceReplace.
	Choice Choice
	// Final, when not nil, receives the location the file really got, relative to
	// Dest.Mount (it differs from Dest.Rel() when keep both chose "name (1).ext").
	Final *string
}

// placedAt tells the caller where the file went.
func (p Placement) placedAt(rel string) {
	if p.Final != nil {
		*p.Final = rel
	}
}

// ErrConflictFolder means a folder stands at the Path. Only skip is possible then:
// replace and keep both apply to files.
var ErrConflictFolder = errors.New("a folder is at that Path")

// errTaken is a link refused because the name exists. It stays inside this package.
var errTaken = errors.New("name taken")

// replacedKept is a late Conflict during Replace: the old file is already in the Trash
// and something else appeared at the Path before the new file could be linked, so the new
// file was not placed (nothing is ever overwritten). It is a Conflict, and says where the
// old file is.
type replacedKept struct{ trash string }

func (e replacedKept) Error() string {
	return "a file appeared at that Path; the replaced file stays in the Trash"
}
func (e replacedKept) Is(target error) bool { return target == ErrConflict }

// Choice is how the User settled a Conflict. Keep and replace are carried in Upload metadata. Skip never is: a
// skipped file is not uploaded, so it is only reported (see the package doc) and accepted
// by the resolve step.
type Choice string

const (
	ChoiceKeep    Choice = "keep"
	ChoiceReplace Choice = "replace"
	ChoiceSkip    Choice = "skip"
)

// known reports whether c is a choice that travels in Upload metadata (or is empty).
func (c Choice) known() bool { return c == "" || c == ChoiceKeep || c == ChoiceReplace }

// linkExclusive hard-links the staged file to rel; rename(2) would silently replace a file
// that appeared meanwhile, link fails with EEXIST instead.
func linkExclusive(root *os.Root, staged, rel string) error {
	if err := root.Link(staged, rel); err != nil {
		switch {
		case errors.Is(err, fs.ErrExist):
			return errTaken
		case errors.Is(err, syscall.ENOTDIR):
			return errParentConflict
		case errors.Is(err, fs.ErrNotExist):
			return resolver.ErrNotFound // the parent folder vanished
		case errors.Is(err, syscall.ENAMETOOLONG):
			return resolver.ErrTooLong // keep both's number can carry a name past NAME_MAX
		}
		return fmt.Errorf("link into place: %w", scrub.Path(err))
	}
	return nil
}

// placeExclusive completes an Upload with a create-exclusive hard link from the Staging
// Area and refuses an existing Path.
func placeExclusive(root *os.Root, p Placement, rel string) error {
	if err := linkExclusive(root, p.Staged, rel); err != nil {
		return takenIsConflict(err)
	}
	p.placedAt(rel)
	return nil
}

// Trasher is the Trash as ChoicePlacer needs it: Locked runs fn under the process-wide
// write lock shared with deletes (trash.Trash.Locked).
type Trasher interface {
	Locked(fn func(place func(mount, rel string) (string, error)) error) error
}

// ChoicePlacer applies the Conflict choice of a Placement:
//
//	none     link exclusively; an existing Path is a Conflict
//	keep     link at the Path, else at "name (1).ext", "name (2).ext", ... (the first free
//	         name, each tried with an exclusive link, so concurrent Uploads cannot clash)
//	replace  under the write lock, move the old file to the Trash, then link exclusively.
//	         If the link fails the old file is restored; if something reappeared at the
//	         Path it is not overwritten, the old file stays in the Trash and a Conflict
//	         comes back (replacedKept).
//
// A folder at the Path is ErrConflictFolder for every choice.
type ChoicePlacer struct {
	Trash Trasher
	// Log receives operational errors; nil discards them.
	Log *slog.Logger
}

// maxKeepBoth bounds the search for a free "name (n).ext".
const maxKeepBoth = 10000

// Place puts a completed Upload at its Path. Missing parent folders are made before it
// runs (makeParents). It never overwrites silently, and returns ErrConflict when it
// declines. The Handler removes the staged name afterwards.
func (c ChoicePlacer) Place(p Placement) error {
	root, err := p.Dest.OS()
	if err != nil {
		return err
	}
	rel := p.Dest.Rel()
	switch p.Choice {
	case "":
		return placeExclusive(root, p, rel)
	case ChoiceKeep:
		return c.keepBoth(root, p, rel)
	case ChoiceReplace:
		return c.replace(root, p, rel)
	}
	return fmt.Errorf("unknown Conflict choice %q", p.Choice)
}

func (c ChoicePlacer) keepBoth(root *os.Root, p Placement, rel string) error {
	if fi, err := root.Lstat(rel); err == nil && fi.IsDir() {
		return ErrConflictFolder
	}
	for n := 0; n < maxKeepBoth; n++ {
		name := numbered(rel, n)
		if !p.Dest.Fits(name) { // os.Root would manage, and nothing could reach the file
			return resolver.ErrTooLong
		}
		err := linkExclusive(root, p.Staged, name)
		if !errors.Is(err, errTaken) {
			if err == nil {
				p.placedAt(name)
			}
			return err
		}
	}
	return ErrConflict
}

// numbered is rel for n == 0, else the Numbered name in the same folder.
func numbered(rel string, n int) string {
	dir, name := path.Split(rel)
	return dir + resolver.Numbered(name, n, false)
}

func (c ChoicePlacer) replace(root *os.Root, p Placement, rel string) error {
	if c.Trash == nil {
		return errors.New("replace: no Trash")
	}
	return c.Trash.Locked(func(place func(mount, rel string) (string, error)) error {
		var trashed string
		fi, err := root.Lstat(rel)
		switch {
		case err == nil && fi.IsDir():
			return ErrConflictFolder
		case err == nil:
			if trashed, err = place(p.Dest.Mount.Name, rel); err != nil && !errors.Is(err, trash.ErrNotFound) {
				return fmt.Errorf("replace: %w", err)
			}
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("replace: %w", scrub.Path(err))
		}
		err = linkExclusive(root, p.Staged, rel)
		if err == nil {
			p.placedAt(rel)
			return nil
		}
		if trashed == "" {
			return takenIsConflict(err)
		}
		if errors.Is(err, errTaken) {
			// Something reappeared at the Path. Never overwrite it, and do not shuffle
			// the old file around: it stays in the Trash and the User is told.
			return replacedKept{trash: trashed}
		}
		// The new file could not be linked: put the old one back.
		if rerr := root.Link(trashed, rel); rerr == nil {
			rerr = root.Remove(trashed)
			if rerr != nil && c.Log != nil {
				c.Log.Error("replace: remove restored copy from the Trash", "err", scrub.Path(rerr))
			}
		} else {
			if c.Log != nil {
				c.Log.Error("replace: restore the old file", "err", scrub.Path(rerr))
			}
			return replacedKept{trash: trashed}
		}
		return takenIsConflict(err)
	})
}

func takenIsConflict(err error) error {
	if errors.Is(err, errTaken) {
		return ErrConflict
	}
	return err
}

// stagedName is where Upload id's data waits, relative to its Mount.
func stagedName(id string) string { return path.Join(mounts.StagingDir, id) }
