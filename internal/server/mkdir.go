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
	// A refused name is the User's to fix: answer on the folder page, the form still open.
	failName := func(err error) {
		s.refuseWith(w, r, "mkdir", user, target.String(), err, false, func(w http.ResponseWriter, r *http.Request, status int, key string) {
			s.mkdirForm(w, r, user, parent, ploc, name, status, key)
		})
	}
	if nameErr != nil {
		failName(nameErr)
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
	if cloc.TooLong {
		fail(resolver.ErrTooLong)
		return
	}
	if cloc.Exists || s.clashes(ploc, child.Name()) {
		failName(errExists)
		return
	}
	root, err := cloc.OS()
	if err == nil {
		err = root.Mkdir(cloc.Rel(), mkdirMode)
	}
	if errors.Is(err, os.ErrExist) {
		failName(errExists)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	s.Audit.Record(audit.Event{User: user, Operation: "mkdir", Path: child.String(), Outcome: audit.OK, Status: http.StatusSeeOther})
	http.Redirect(w, r, folderHref(parent), http.StatusSeeOther)
}

// mkdirForm answers a refused name with the folder page and the form open on the error.
// A folder that can no longer be listed gets the plain error page.
func (s *server) mkdirForm(w http.ResponseWriter, r *http.Request, user string, p resolver.Path, loc resolver.Location, name string, status int, key string) {
	entries, err := loc.ReadDir()
	if err != nil {
		s.errorPage(w, r, status, key)
		return
	}
	v := s.folderView(r, user, p, loc, entries)
	v.MkdirError, v.MkdirName = key, name
	s.render(w, r, status, "browse.html", page{Title: p.Name(), Browse: v})
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
