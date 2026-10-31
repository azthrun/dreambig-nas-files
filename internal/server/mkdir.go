package server

import (
	"errors"
	"net/http"
	"os"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// errExists is a create that would land on a name already taken (409).
var errExists = errors.New("name already exists")

// mkdirMode is the folder mode before the process umask.
const mkdirMode = 0o775

// mkdir creates a folder: POST /files/<parent Path> with form fields op=mkdir and
// name. It works as a plain HTML form (no JavaScript) and answers 303 back to the
// parent's listing. Order matters: no Access to the parent looks like a missing Path
// (404) before anything about the name is judged.
func (s *server) mkdir(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	name := r.PostFormValue("name")
	target := resolver.Path{} // what the audit line names: the new folder if it can be formed

	fail := func(err error) {
		s.refuse(w, r, "mkdir", user, target.String(), err, false)
	}
	parent, err := pathFromRequest(r)
	if err != nil {
		fail(err)
		return
	}
	target = parent
	ploc, err := s.res.Resolve(parent)
	if err != nil {
		fail(err)
		return
	}
	if err = s.authorize(user, ploc, access.Read); err != nil {
		fail(err)
		return
	}
	if !ploc.IsRoot() {
		if fi, serr := ploc.Stat(); serr != nil || !fi.IsDir() {
			fail(resolver.ErrNotFolder)
			return
		}
	}
	child, nameErr := parent.JoinNew(name)
	if nameErr == nil {
		target = child
	}
	if !s.canWriteIn(user, ploc) { // the Root, read Access, or a read-only Mount
		fail(errForbidden)
		return
	}
	if nameErr != nil {
		fail(nameErr)
		return
	}
	cloc, err := s.res.Resolve(child)
	if err != nil {
		fail(err)
		return
	}
	// The new Path's real Mount decides: a name that is a link into another Mount, or
	// into a Mount folder itself, is judged there.
	if err = s.authorize(user, cloc, access.Write); err != nil {
		fail(err)
		return
	}
	if cloc.Exists || s.clashes(ploc, child.Name()) {
		fail(errExists)
		return
	}
	root, err := cloc.OS()
	if err == nil {
		err = root.Mkdir(cloc.Rel(), mkdirMode)
	}
	if errors.Is(err, os.ErrExist) {
		err = errExists
	}
	if err != nil {
		fail(err)
		return
	}
	s.Audit.Record(audit.Event{User: user, Operation: "mkdir", Path: child.String(), Outcome: audit.OK, Status: http.StatusSeeOther})
	http.Redirect(w, r, folderHref(parent), http.StatusSeeOther)
}

// clashes reports whether the folder already holds a name that a case-insensitive
// client (Windows, macOS) would treat as the same as name.
func (s *server) clashes(dir resolver.Location, name string) bool {
	entries, err := dir.ReadDir()
	if err != nil {
		return false
	}
	want := mounts.Fold(name)
	for _, e := range entries {
		if mounts.Fold(e.Name) == want {
			return true
		}
	}
	return false
}
