package server

import (
	"context"
	"errors"
	"io/fs"
	"mime"
	"net/http"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/download"
	"github.com/azthrun/dreambig-nas-files/internal/i18n"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/scrub"
)

// zipFolder streams the folder at p (already resolved, authorized and known to be a
// folder) as one zip: GET /files/<Path>?zip=1.
func (s *server) zipFolder(w http.ResponseWriter, r *http.Request, user string, p resolver.Path, loc resolver.Location) {
	s.streamZip(w, r, user, p, p.Name(), []download.Item{{Name: p.Name(), Path: p, Loc: loc}})
}

// zipSelection handles the browse form's "zip" operation:
// POST /files/<folder> with op=zip and one names=<entry> per ticked row. Read Access
// is enough: a download is not a write. Every name goes through the Path resolver and
// authorize like any other request; names that are hidden, missing, out of reach or
// neither a file nor a folder are dropped as if they were not there, and each drop is a
// refusal in the audit log.
func (s *server) zipSelection(w http.ResponseWriter, r *http.Request, user string, p resolver.Path) {
	loc, err := s.res.Resolve(p)
	if err == nil {
		err = s.authorize(user, loc, access.Read)
	}
	if err == nil && loc.IsRoot() {
		err = resolver.ErrNotFound
	}
	if err == nil {
		var fi fs.FileInfo
		if fi, err = loc.Stat(); err == nil && !fi.IsDir() {
			err = resolver.ErrNotFolder
		}
	}
	if err != nil {
		s.refuse(w, r, "zip", user, p.String(), err, false)
		return
	}
	picked := r.PostForm["names"]
	if len(picked) == 0 {
		s.refuse(w, r, "zip", user, p.String(), errNothingSelected, false)
		return
	}
	var items []download.Item
	seen := map[string]bool{}
	for _, n := range picked {
		child, err := p.Join(n)
		if err == nil && seen[child.String()] {
			continue
		}
		var cl resolver.Location
		if err == nil {
			seen[child.String()] = true
			if cl, err = s.res.Resolve(child); err == nil && !cl.Exists {
				err = resolver.ErrNotFound
			}
		}
		if err == nil {
			err = s.authorize(user, cl, access.Read)
		}
		if err == nil {
			if fi, serr := cl.Stat(); serr == nil && !fi.IsDir() && !fi.Mode().IsRegular() {
				err = errNotFile
			}
		}
		if err != nil {
			s.dropFromZip(user, p, child, err)
			continue
		}
		items = append(items, download.Item{Name: child.Name(), Path: child, Loc: cl})
	}
	if len(items) == 0 { // each name has its line already
		s.refuse(w, r, "zip", user, p.String(), resolver.ErrNotFound, true)
		return
	}
	s.streamZip(w, r, user, p, p.Name(), items)
}

// dropFromZip audits one selected name that is left out of the zip. A string that is not
// a valid Path (child is then empty) is never recorded; the line names the folder.
func (s *server) dropFromZip(user string, folder, child resolver.Path, err error) {
	path := child.String()
	if child.IsRoot() {
		path = folder.String()
	}
	s.Audit.Record(audit.Event{User: user, Operation: "zip", Path: path, Outcome: audit.Refused, Status: statusOf(err)})
}

// streamZip writes the zip while walking: headers sent at once, the work ended by the
// request context when the client goes away. The walk itself is download.Zip.
func (s *server) streamZip(w http.ResponseWriter, r *http.Request, user string, audited resolver.Path, base string, items []download.Item) {
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": base + ".zip"}))
	h.Set("Cache-Control", "private, no-cache")
	h.Set("X-Accel-Buffering", "no")
	cw := &countingWriter{ResponseWriter: w}
	cw.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush() // the download starts now, before the first file is read
	}

	var omitted int
	var err error
	if r.Method != http.MethodHead {
		allow := func(loc resolver.Location) bool { return s.authorize(user, loc, access.Read) == nil }
		omitted, err = download.Zip(r.Context(), cw, s.res, allow, s.Clock.Now(), i18n.T(langOf(r), "zip.incomplete"), items)
	}
	ev := audit.Event{User: user, Operation: "zip", Path: audited.String(), Outcome: audit.OK, Status: cw.status, Bytes: cw.bytes}
	for _, it := range items {
		if it.Path.String() != audited.String() { // a whole folder is just its Path
			ev.Items = append(ev.Items, it.Path.String())
		}
	}
	switch {
	case err != nil: // client gone or a read failed: the archive is deliberately left unfinished
		ev.Outcome = audit.Failed
		if !errors.Is(err, context.Canceled) {
			s.Log.Error("zip", "err", scrub.Path(err))
		}
	case omitted > 0:
		ev.Outcome = audit.Partial
		ev.Omitted = omitted
	}
	s.Audit.Record(ev)
}
