package server

import (
	"errors"
	"io/fs"
	"net/http"
	"slices"

	"golang.org/x/text/unicode/norm"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
)

// Selection forms. The listing's checkbox form posts to /select with:
//
//	folder  the Path of the folder shown (plain, e.g. "docs/tax"); "" is the Root
//	name    one per ticked entry: its name inside that folder
//	action  which submit button was pressed
//
// selectItems dispatches on action. Delete shows a confirm page (plain HTML, no
// JavaScript) whose form posts the same folder and names to /delete. A later action
// (zip download) adds a case in selectItems and a button in browse.html.

var errNothingSelected = errors.New("nothing selected")

// selectedItem is one ticked entry of the folder, resolved as the entry itself: a
// symlink is the link, never its target.
type selectedItem struct {
	Name string        // as shown (NFC)
	Path resolver.Path // folder + Name
	// loc is the entry inside the folder's real Mount: Real ends with the name stored on
	// disk, so Mount and Rel() point at the link itself.
	loc resolver.Location
}

type selection struct {
	Folder resolver.Path
	Items  []selectedItem
}

// selectItems is POST /select.
func (s *server) selectItems(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.bad_path")
		return
	}
	switch r.PostForm.Get("action") {
	case "delete":
		s.confirmDelete(w, r)
	default:
		s.errorPage(w, r, http.StatusBadRequest, "error.method")
	}
}

type confirmView struct {
	Folder string
	Names  []string
	Back   string // where Cancel goes
}

// confirmDelete shows the items about to go to the Trash and asks to confirm.
func (s *server) confirmDelete(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	sel, auditPath, err := s.selected(r, user)
	if err != nil {
		s.refuseDelete(w, r, user, auditPath, err)
		return
	}
	v := &confirmView{Folder: sel.Folder.String(), Back: folderHref(sel.Folder)}
	for _, it := range sel.Items {
		v.Names = append(v.Names, it.Name)
	}
	s.render(w, r, http.StatusOK, "delete.html", page{Title: sel.Folder.Name(), Confirm: v, Next: v.Back})
}

// deleteItems is POST /delete: move every named item to its Mount's Trash. All names
// are checked before anything moves, so a bad name changes nothing.
func (s *server) deleteItems(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.bad_path")
		return
	}
	sel, auditPath, err := s.selected(r, user)
	if err != nil {
		s.refuseDelete(w, r, user, auditPath, err)
		return
	}
	for _, it := range sel.Items {
		if _, err := s.Trash.Place(it.loc.Mount.Name, it.loc.Rel()); err != nil {
			s.audit.Record(audit.Event{User: user, Operation: "delete", Path: it.Path.String(), Outcome: audit.Failed, Status: statusOfDelete(err)})
			s.refuseDelete(w, r, user, it.Path.String(), err)
			return
		}
		s.audit.Record(audit.Event{User: user, Operation: "delete", Path: it.Path.String(), Outcome: audit.OK, Status: http.StatusSeeOther})
	}
	http.Redirect(w, r, folderHref(sel.Folder), http.StatusSeeOther)
}

// selected resolves and authorises the posted folder and names for a change: every item
// must be visible to the User, writable, and not a Mount or Hidden Entry. On error the
// returned string is the Path to put in the audit line.
func (s *server) selected(r *http.Request, user string) (*selection, string, error) {
	folder, err := resolver.Parse(r.PostForm.Get("folder"))
	if err != nil {
		return nil, "", err
	}
	auditPath := folder.String()
	loc, err := s.res.Resolve(folder)
	if err != nil {
		return nil, auditPath, err
	}
	if loc.IsRoot() { // the Root's entries are the Mounts, which are never deleted
		return nil, auditPath, s.authorize(user, loc, access.Write)
	}
	if err := s.authorize(user, loc, access.Read); err != nil {
		return nil, auditPath, err
	}
	entries, err := loc.ReadDir() // what the User can see here; also proves loc is a folder
	if err != nil {
		return nil, auditPath, err
	}
	visible := map[string]bool{}
	for _, e := range entries {
		if s.mountReadable(user, e.Mount) {
			visible[e.Name] = true
		}
	}
	if len(r.PostForm["name"]) == 0 {
		return nil, auditPath, errNothingSelected
	}
	onDisk, err := diskNames(loc)
	if err != nil {
		return nil, auditPath, err
	}
	sel := &selection{Folder: folder}
	for _, name := range r.PostForm["name"] {
		if slices.ContainsFunc(sel.Items, func(it selectedItem) bool { return it.Name == norm.NFC.String(name) }) {
			continue
		}
		p, err := folder.Join(name)
		if err != nil {
			return nil, auditPath, err
		}
		auditPath = p.String()
		if mounts.IsHiddenName(p.Name()) {
			return nil, auditPath, resolver.ErrHidden
		}
		disk, ok := onDisk[p.Name()]
		if !ok || !visible[p.Name()] {
			return nil, auditPath, resolver.ErrNotFound
		}
		item := loc
		item.Real = append(slices.Clone(loc.Real), disk)
		item.Path, item.Exists = p, true
		// Judged by the folder's real Mount: a link is changed in the Mount it lives in,
		// whatever it points at.
		if err := s.authorize(user, item, access.Write); err != nil {
			return nil, auditPath, err
		}
		sel.Items = append(sel.Items, selectedItem{Name: p.Name(), Path: p, loc: item})
	}
	return sel, folder.String(), nil
}

// diskNames maps the NFC names a User sees in the folder to the names stored on disk.
func diskNames(loc resolver.Location) (map[string]string, error) {
	f, err := loc.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(des))
	for _, de := range des {
		nfc := norm.NFC.String(de.Name())
		if _, dup := out[nfc]; !dup || nfc == de.Name() {
			out[nfc] = de.Name()
		}
	}
	return out, nil
}

// statusOfDelete maps every error of the delete flow to its HTTP status.
func statusOfDelete(err error) int {
	switch {
	case errors.Is(err, errNothingSelected):
		return http.StatusBadRequest
	case errors.Is(err, trash.ErrReadOnly):
		return http.StatusForbidden
	case errors.Is(err, trash.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, trash.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound
	}
	return statusOf(err)
}

// refuseDelete audits and answers a delete (or its confirmation) that was refused.
func (s *server) refuseDelete(w http.ResponseWriter, r *http.Request, user, auditPath string, err error) {
	status := statusOfDelete(err)
	if status == http.StatusInternalServerError {
		s.Log.Error("delete", "err", err)
		s.errorPage(w, r, status, "error.internal")
		return
	}
	ev := audit.Event{User: user, Operation: "delete", Outcome: audit.Refused, Status: status}
	if !errors.Is(err, resolver.ErrInvalidPath) {
		ev.Path = auditPath // only a syntactically valid Path is ever recorded
	}
	s.audit.Record(ev)
	key := "error.not_found"
	switch status {
	case http.StatusBadRequest:
		key = "error.bad_path"
		if errors.Is(err, errNothingSelected) {
			key = "error.nothing_selected"
		}
	case http.StatusForbidden:
		key = "error.read_only"
	}
	s.errorPage(w, r, status, key)
}
