package server

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/listing"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// Move is two steps of the browse selection form (POST /files/<folder>, see filesPost).
// op=move shows a folder picker for the ticked names=<entry>, starting at their folder;
// each folder in it is a submit button posting op=move again with at=<Path>, so it works
// without JavaScript. Its "Move here" posts op=move-confirm with dest=<Path>.
//
// Moves stay inside one Mount, so they are atomic renames (ADR-0005).

var (
	// errOtherMount is a destination outside the items' Mount.
	errOtherMount = errors.New("destination is in another Mount")
	// errIntoItself is a folder moved into itself or one of its folders.
	errIntoItself = errors.New("folder moved into itself")
	// errRelativeLink is a symlink whose relative target would dangle at the destination.
	errRelativeLink = errors.New("link with a relative target")
)

// pickerFolder is one place the picker can walk to.
type pickerFolder struct {
	Name string
	Path string // value of the at field
}

type moveView struct {
	Items   []confirmItem
	Crumbs  []pickerFolder // the Mount down to At; the last one is At
	Folders []pickerFolder // At's folders
	At      string         // the Path being looked at
	Here    bool           // At is where the items already are
	Back    string         // the source folder: the forms post here, Cancel goes here
	Error   string         // i18n key of a refused move
}

// mountPath is the real location of loc written as a Path. The picker uses it rather than
// the Path as sent, which may run through a link from another Mount: its parents would
// lead out of the Mount.
func mountPath(loc resolver.Location) (resolver.Path, error) {
	return resolver.ParseSegments(append([]string{loc.Mount.Name}, loc.Real...))
}

// moveFolder resolves a picker or dest Path and checks it is a folder in the items' Mount.
func (s *server) moveFolder(user string, sel *selection, raw string) (resolver.Location, error) {
	p, err := resolver.Parse(raw)
	if err != nil {
		return resolver.Location{}, err
	}
	loc, err := s.res.Resolve(p)
	if err != nil {
		return resolver.Location{}, err
	}
	if err := s.authorize(user, loc, access.Read); err != nil {
		return resolver.Location{}, err
	}
	if loc.IsRoot() || loc.Mount.Name != sel.loc.Mount.Name {
		return resolver.Location{}, errOtherMount
	}
	if fi, err := loc.Stat(); err != nil {
		return resolver.Location{}, err
	} else if !fi.IsDir() {
		return resolver.Location{}, resolver.ErrNotFolder
	}
	return loc, nil
}

func (s *server) movePicker(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	sel, auditPath, err := s.selected(r, user)
	if err != nil {
		s.refuse(w, r, "move", user, auditPath, err, false)
		return
	}
	at := sel.loc
	if raw, ok := r.PostForm["at"]; ok && len(raw) > 0 {
		if at, err = s.moveFolder(user, sel, raw[0]); err != nil {
			s.refuse(w, r, "move", user, auditPath, err, false)
			return
		}
	}
	s.renderPicker(w, r, sel, at, http.StatusOK, "")
}

// renderPicker shows the picker at the folder at. A folder that can no longer be listed
// gets the plain error page.
func (s *server) renderPicker(w http.ResponseWriter, r *http.Request, sel *selection, at resolver.Location, status int, key string) {
	atPath, err := mountPath(at)
	var entries []resolver.Entry
	if err == nil {
		entries, err = at.ReadDir()
	}
	if err != nil {
		s.refuse(w, r, "move", sessionOf(r).User, atPath.String(), err, false)
		return
	}
	v := &moveView{At: atPath.String(), Here: sameFolder(at, sel.loc), Back: folderHref(sel.Folder), Error: key}
	for _, it := range sel.Items {
		fi, err := it.loc.Stat()
		v.Items = append(v.Items, confirmItem{Name: it.Name, IsDir: err == nil && fi.IsDir()})
	}
	segs := atPath.Segments()
	for i, name := range segs {
		if p, err := resolver.ParseSegments(segs[:i+1]); err == nil {
			v.Crumbs = append(v.Crumbs, pickerFolder{Name: name, Path: p.String()})
		}
	}
	listing.Sort(entries, listing.ByName, listing.Asc)
	for _, e := range entries {
		if !e.IsDir || e.Mount != at.Mount.Name || (v.Here && slices.ContainsFunc(sel.Items, func(it selectedItem) bool { return it.Name == e.Name })) {
			continue
		}
		if p, err := atPath.Join(e.Name); err == nil {
			v.Folders = append(v.Folders, pickerFolder{Name: e.Name, Path: p.String()})
		}
	}
	s.render(w, r, status, "move.html", page{Title: sel.Folder.Name(), Move: v, Next: v.Back})
}

func sameFolder(a, b resolver.Location) bool {
	return a.Mount.Name == b.Mount.Name && slices.Equal(a.Real, b.Real)
}

// plannedMove is one item with its place in the destination.
type plannedMove struct {
	item selectedItem
	rel  string        // inside the Mount, with the name stored on disk
	path resolver.Path // what the audit line names
}

// moveItems is op=move-confirm. All items are checked before anything moves, so a clash
// on the last one changes nothing.
func (s *server) moveItems(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	sel, auditPath, err := s.selected(r, user)
	if err != nil {
		s.refuse(w, r, "move", user, auditPath, err, false)
		return
	}
	dest, err := s.moveFolder(user, sel, r.PostFormValue("dest"))
	if err == nil && !s.canWriteIn(user, dest) {
		err = errForbidden
	}
	if err != nil {
		s.refuse(w, r, "move", user, auditPath, err, false)
		return
	}
	plan, failed, err := s.planMove(sel, dest)
	fail := func(where string, err error) {
		s.refuseWith(w, r, "move", user, where, err, false, func(w http.ResponseWriter, r *http.Request, status int, key string) {
			s.renderPicker(w, r, sel, dest, status, key)
		})
	}
	if err != nil {
		fail(failed, err)
		return
	}
	for _, m := range plan {
		if err := s.Trash.Move(dest.Mount.Name, m.item.loc.Rel(), m.rel); err != nil {
			fail(m.item.Path.String(), err)
			return
		}
		s.Audit.Record(audit.Event{User: user, Operation: "move", Path: m.path.String(), From: m.item.Path.String(), Outcome: audit.OK, Status: http.StatusSeeOther})
	}
	http.Redirect(w, r, folderHref(sel.Folder), http.StatusSeeOther)
}

// planMove checks every item against dest. On error the string is the item's Path.
func (s *server) planMove(sel *selection, dest resolver.Location) ([]plannedMove, string, error) {
	destPath, err := mountPath(dest)
	if err != nil {
		return nil, "", err
	}
	plan := make([]plannedMove, 0, len(sel.Items))
	seen := map[string]bool{}
	for _, it := range sel.Items {
		from := it.Path.String()
		if err := refuseLinkOrSelf(it, dest); err != nil {
			return nil, from, err
		}
		fold := mounts.Fold(it.Name)
		if s.clashes(dest, it.Name) || seen[fold] {
			return nil, from, errExists
		}
		seen[fold] = true
		rel := path.Join(dest.Rel(), it.loc.Real[len(it.loc.Real)-1])
		if !dest.Fits(rel) {
			return nil, from, resolver.ErrTooLong
		}
		p, err := destPath.Join(it.Name)
		if err != nil {
			return nil, from, err
		}
		plan = append(plan, plannedMove{item: it, rel: rel, path: p})
	}
	return plan, "", nil
}

// refuseLinkOrSelf refuses a folder going into itself and a symlink a move would break.
// A relative target means something else from another folder, and a link that dangles
// disappears from the listing. An absolute target stays right wherever the link is.
func refuseLinkOrSelf(it selectedItem, dest resolver.Location) error {
	root, err := it.loc.OS()
	if err != nil {
		return err
	}
	fi, err := root.Lstat(it.loc.Rel())
	if err != nil {
		return statMiss(err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := root.Readlink(it.loc.Rel())
		if err != nil {
			return statMiss(err)
		}
		if !strings.HasPrefix(target, "/") {
			return errRelativeLink
		}
		return nil
	}
	if len(dest.Real) >= len(it.loc.Real) && slices.Equal(dest.Real[:len(it.loc.Real)], it.loc.Real) {
		return errIntoItself
	}
	return nil
}

// statMiss treats an entry gone since the selection as a miss.
func statMiss(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return resolver.ErrNotFound
	}
	return err
}
