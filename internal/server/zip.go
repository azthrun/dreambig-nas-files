package server

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// maxZipDepth bounds the walk as a second guard next to the loop check.
const maxZipDepth = 64

// zipFolder streams the folder at p (already resolved, authorized and known to be a
// folder) as one zip: GET /files/<Path>?zip=1.
func (s *server) zipFolder(w http.ResponseWriter, r *http.Request, user string, p resolver.Path, loc resolver.Location) {
	s.streamZip(w, r, user, p, p.Name(), []zipItem{{name: p.Name(), path: p, loc: loc}})
}

// zipSelection handles the browse form's "zip" action:
// POST /files/<folder> with action=zip and one names=<entry> per ticked row. Read Access
// is enough: a download is not a write. Every name goes through the Path resolver and
// authorize like any other request; names that are hidden, missing or out of reach are
// dropped as if they were not there.
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
		s.refuseBrowse(w, r, user, p, err)
		return
	}
	picked := r.PostForm["names"]
	if len(picked) == 0 {
		s.errorPage(w, r, http.StatusBadRequest, "error.no_selection")
		return
	}
	var items []zipItem
	seen := map[string]bool{}
	for _, n := range picked {
		child, err := p.Join(n)
		if err != nil || seen[child.String()] {
			continue
		}
		seen[child.String()] = true
		cl, err := s.res.Resolve(child)
		if err != nil || !cl.Exists || s.authorize(user, cl, access.Read) != nil {
			continue
		}
		items = append(items, zipItem{name: child.Name(), path: child, loc: cl})
	}
	if len(items) == 0 {
		s.refuseBrowse(w, r, user, p, resolver.ErrNotFound)
		return
	}
	s.streamZip(w, r, user, p, p.Name(), items)
}

// zipItem is one top-level thing to archive, under the name it gets in the zip.
type zipItem struct {
	name string
	path resolver.Path
	loc  resolver.Location
}

// streamZip writes the zip while walking: store-only, no temp files, headers sent at
// once. The request context ends the work when the client goes away.
func (s *server) streamZip(w http.ResponseWriter, r *http.Request, user string, audited resolver.Path, base string, items []zipItem) {
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

	var err error
	if r.Method != http.MethodHead {
		z := &zipWalk{s: s, user: user, ctx: r.Context(), zw: zip.NewWriter(cw)}
		for _, it := range items {
			if err = z.add(it.path, it.loc, it.name, nil); err != nil {
				break
			}
		}
		if err == nil {
			err = z.zw.Close()
		}
	}
	ev := audit.Event{User: user, Operation: "zip", Path: audited.String(), Outcome: audit.OK, Status: cw.status, Bytes: cw.bytes}
	if err != nil { // client gone or a read failed: the archive is deliberately left unfinished
		ev.Outcome = audit.Failed
		if !errors.Is(err, context.Canceled) {
			s.Log.Error("zip", "err", err)
		}
	}
	s.audit.Record(ev)
}

type zipWalk struct {
	s    *server
	user string
	ctx  context.Context
	zw   *zip.Writer
}

// add archives the thing at p/loc as name, descending into folders. chain holds the real
// folders being walked (Mount + location), so a symlink back into one of them is
// skipped instead of followed forever.
func (z *zipWalk) add(p resolver.Path, loc resolver.Location, name string, chain []string) error {
	if err := z.ctx.Err(); err != nil {
		return err
	}
	fi, err := loc.Stat()
	if err != nil {
		return nil // vanished meanwhile, or no longer valid: leave it out
	}
	if fi.IsDir() {
		key := loc.Mount.Name + "\x00" + loc.Rel()
		for _, k := range chain {
			if k == key {
				return nil // a symlink loop
			}
		}
		if len(chain) >= maxZipDepth {
			return nil
		}
		hdr := &zip.FileHeader{Name: name + "/", Method: zip.Store, Modified: fi.ModTime()}
		hdr.SetMode(fi.Mode())
		if _, err := z.zw.CreateHeader(hdr); err != nil {
			return err
		}
		entries, err := loc.ReadDir()
		if err != nil {
			return nil
		}
		chain = append(chain[:len(chain):len(chain)], key)
		for _, e := range entries {
			child, err := p.Join(e.Name)
			if err != nil {
				continue
			}
			// Each entry is judged on its own: the resolver follows its symlinks to the
			// real target (Hidden, outside the Root), authorize checks that target's Mount.
			cl, err := z.s.res.Resolve(child)
			if err != nil || !cl.Exists || z.s.authorize(z.user, cl, access.Read) != nil {
				continue
			}
			if err := z.add(child, cl, name+"/"+e.Name, chain); err != nil {
				return err
			}
		}
		return nil
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	f, err := loc.Open()
	if err != nil {
		return nil
	}
	defer f.Close()
	hdr := &zip.FileHeader{Name: name, Method: zip.Store, Modified: fi.ModTime()}
	hdr.SetMode(fi.Mode())
	dst, err := z.zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, &ctxReader{ctx: z.ctx, r: f})
	return err
}

// ctxReader stops a copy as soon as the request is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}
