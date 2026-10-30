package upload

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"syscall"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// ErrConflict means the Path already holds something (or a file stands where a folder
// should be). It is what a Placer returns to refuse; the User gets 409.
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
}

// Placer is the completion seam. The default, LinkPlacer, refuses an existing Path
// (ADR-0003 note). #12 supplies one that applies the User's Conflict choice (skip, keep
// both, replace to Trash); #13 one that also creates missing parent folders. A Placer
// must never overwrite silently, and returns ErrConflict when it declines. The caller
// removes the staged name afterwards.
type Placer interface {
	Place(ctx context.Context, p Placement) error
}

// LinkPlacer completes an Upload with a create-exclusive hard link from the Staging
// Area. rename(2) would silently replace a file that appeared meanwhile; link fails
// with EEXIST instead, so a late Conflict is refused rather than overwritten.
type LinkPlacer struct{}

// Place implements Placer.
func (LinkPlacer) Place(_ context.Context, p Placement) error {
	root, err := p.Dest.OS()
	if err != nil {
		return err
	}
	if err := root.Link(p.Staged, p.Dest.Rel()); err != nil {
		switch {
		case errors.Is(err, fs.ErrExist), errors.Is(err, syscall.ENOTDIR):
			return ErrConflict
		case errors.Is(err, fs.ErrNotExist):
			return resolver.ErrNotFound // the parent folder vanished
		}
		return fmt.Errorf("link into place: %w", scrubPath(err))
	}
	return nil
}

// stagedName is where Upload id's data waits, relative to its Mount.
func stagedName(id string) string { return path.Join(mounts.StagingDir, id) }

// scrubPath drops OS paths from link errors so they never reach a User.
func scrubPath(err error) error {
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}
