package server

import (
	"errors"
	"fmt"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// errForbidden is a User with Access to a Mount asking for more than they have (a write
// with read Access, or any change to the Root or a Mount itself). Map it to 403 with
// refuse. A User with no Access never sees it: that is errNoAccess (404).
var errForbidden = errors.New("not allowed")

// errNoAccess is a User with no Access to the Mount asking for it. It is a not-found
// (404, the response is identical to a missing Path) but, unlike a plain miss, it is
// audited.
var errNoAccess = fmt.Errorf("no Access: %w", resolver.ErrNotFound)

// authorize is THE Access check: call it in every handler right after Resolve and before
// any I/O or any other lookup. It judges the REAL target's Mount (loc.Mount, after
// symlinks), so a link into a Mount the User cannot reach fails like a missing Path.
//
//   - need access.Read: browse, download, zip. No Access -> errNoAccess.
//   - need access.Write: mkdir, Upload, delete, replace, rename, move; loc is the thing being created,
//     changed or removed (for Upload or mkdir, the new Path resolved with Exists false).
//     No Access -> errNoAccess; only read Access, a read-only Mount, or loc being
//     the Root or a Mount folder itself (top level is read-only) -> errForbidden.
//
// Handlers turn the error into a response with refuse.
func (s *server) authorize(user string, loc resolver.Location, need access.Level) error {
	if loc.IsRoot() {
		if need >= access.Write {
			return errForbidden
		}
		return nil
	}
	have := s.Access.Level(user, loc.Mount)
	switch {
	case have < access.Read:
		return errNoAccess
	case have < need, need >= access.Write && len(loc.Real) == 0:
		return errForbidden
	}
	return nil
}

// canWriteIn reports whether the User may create things inside the folder loc: loc is
// the Mount folder or below it, and the User has write Access in the folder's real
// Mount (a read-only Mount never allows it). Use it for the parent folder of a create or
// Upload, and to decide whether to render write controls. It is not a security check by
// itself: the write handler must still call authorize on the target.
func (s *server) canWriteIn(user string, loc resolver.Location) bool {
	return !loc.IsRoot() && s.Access.Level(user, loc.Mount) >= access.Write
}

// mountReadable reports whether the User has Access to the Mount called name.
func (s *server) mountReadable(user, name string) bool {
	for _, m := range s.Mounts {
		if m.Name == name {
			return s.Access.Level(user, m) >= access.Read
		}
	}
	return false
}
