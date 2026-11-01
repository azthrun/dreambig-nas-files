package server

import (
	"errors"
	"net/http"
	"path"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// Rename is two steps of the browse selection form (POST /files/<folder>, see filesPost):
// op=rename shows a page asking for the new name of the one ticked names=<entry>; its form
// posts names and name with op=rename-confirm. A refused name answers on that page.

// errRenameOne is a rename of more than one ticked item.
var errRenameOne = errors.New("select exactly one item to rename")

type renameView struct {
	Old   string
	Name  string // what the field holds
	Back  string // the folder: the form posts here, Cancel goes here
	Error string // i18n key of a refused name
}

// renamed resolves the selection and insists on one item. On error the returned string is
// the Path to put in the audit line.
func (s *server) renamed(r *http.Request, user string) (*selection, string, error) {
	sel, auditPath, err := s.selected(r, user)
	if err != nil {
		return nil, auditPath, err
	}
	if len(sel.Items) != 1 {
		return nil, auditPath, errRenameOne
	}
	return sel, auditPath, nil
}

func (s *server) renameForm(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	sel, auditPath, err := s.renamed(r, user)
	if err != nil {
		s.refuse(w, r, "rename", user, auditPath, err, false)
		return
	}
	old := sel.Items[0].Name
	s.renderRename(w, r, sel, http.StatusOK, old, "")
}

func (s *server) renderRename(w http.ResponseWriter, r *http.Request, sel *selection, status int, name, key string) {
	v := &renameView{Old: sel.Items[0].Name, Name: name, Back: folderHref(sel.Folder), Error: key}
	s.render(w, r, status, "rename.html", page{Title: v.Old, Rename: v, Next: v.Back})
}

// renameItem is op=rename-confirm. The new name lands in the item's real folder, so
// Access was settled by selected. An unchanged name changes nothing.
func (s *server) renameItem(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	sel, auditPath, err := s.renamed(r, user)
	if err != nil {
		s.refuse(w, r, "rename", user, auditPath, err, false)
		return
	}
	it, name := sel.Items[0], r.PostFormValue("name")
	target := it.Path
	failName := func(err error) {
		s.refuseWith(w, r, "rename", user, target.String(), err, false, func(w http.ResponseWriter, r *http.Request, status int, key string) {
			s.renderRename(w, r, sel, status, name, key)
		})
	}
	child, err := sel.Folder.JoinNew(name)
	if err != nil {
		failName(err)
		return
	}
	target = child
	if child.Name() == it.Name {
		http.Redirect(w, r, folderHref(sel.Folder), http.StatusSeeOther)
		return
	}
	rel := path.Join(sel.loc.Rel(), child.Name())
	if !sel.loc.Fits(rel) {
		failName(resolver.ErrTooLong)
		return
	}
	if s.clashesWithOther(sel.loc, child.Name(), it.Name) {
		failName(errExists)
		return
	}
	if err := s.Trash.Move(it.loc.Mount.Name, it.loc.Rel(), rel); err != nil {
		failName(err)
		return
	}
	s.Audit.Record(audit.Event{User: user, Operation: "rename", Path: child.String(), From: it.Path.String(), Outcome: audit.OK, Status: http.StatusSeeOther})
	http.Redirect(w, r, folderHref(sel.Folder), http.StatusSeeOther)
}
