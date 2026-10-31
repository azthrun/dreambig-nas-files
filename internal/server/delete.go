package server

import (
	"errors"
	"net/http"
	"slices"

	"golang.org/x/text/unicode/norm"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// Delete is two steps of the browse selection form (POST /files/<folder>, see
// filesPost): op=delete shows a confirm page (plain HTML, no JavaScript) listing the
// ticked names=<entry> values; its form posts the same names with op=delete-confirm,
// which moves them to the Trash.

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

type confirmItem struct {
	Name  string
	IsDir bool
}

type confirmView struct {
	Items []confirmItem
	Back  string // where Cancel goes
}

// confirmDelete shows the items about to go to the Trash and asks to confirm.
func (s *server) confirmDelete(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	sel, auditPath, err := s.selected(r, user)
	if err != nil {
		s.refuse(w, r, "delete", user, auditPath, err, false)
		return
	}
	v := &confirmView{Back: folderHref(sel.Folder)}
	for _, it := range sel.Items {
		fi, err := it.loc.Stat()
		v.Items = append(v.Items, confirmItem{Name: it.Name, IsDir: err == nil && fi.IsDir()})
	}
	s.render(w, r, http.StatusOK, "delete.html", page{Title: sel.Folder.Name(), Confirm: v, Next: v.Back})
}

// deleteItems is op=delete-confirm: move every named item to its Mount's Trash. All
// names are checked before anything moves, so a bad name changes nothing.
func (s *server) deleteItems(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	sel, auditPath, err := s.selected(r, user)
	if err != nil {
		s.refuse(w, r, "delete", user, auditPath, err, false)
		return
	}
	for _, it := range sel.Items {
		if _, err := s.Trash.Place(it.loc.Mount.Name, it.loc.Rel()); err != nil {
			s.refuse(w, r, "delete", user, it.Path.String(), err, false)
			return
		}
		s.Audit.Record(audit.Event{User: user, Operation: "delete", Path: it.Path.String(), Outcome: audit.OK, Status: http.StatusSeeOther})
	}
	http.Redirect(w, r, folderHref(sel.Folder), http.StatusSeeOther)
}

// selected resolves and authorises the posted folder and names for a change: every item
// must be visible to the User, writable, and not a Mount or Hidden Entry. On error the
// returned string is the Path to put in the audit line.
func (s *server) selected(r *http.Request, user string) (*selection, string, error) {
	folder, err := pathFromRequest(r)
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
	if len(r.PostForm["names"]) == 0 {
		return nil, auditPath, errNothingSelected
	}
	onDisk, err := loc.DiskNames()
	if err != nil {
		return nil, auditPath, err
	}
	sel := &selection{Folder: folder}
	for _, name := range r.PostForm["names"] {
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
