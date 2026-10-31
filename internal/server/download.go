package server

import (
	"io/fs"
	"mime"
	"net/http"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// countingWriter records the status and body bytes actually sent, for the audit line.
type countingWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (c *countingWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	n, err := c.ResponseWriter.Write(b)
	c.bytes += int64(n)
	return n, err
}

// download sends the file at loc as an attachment under its original name. The caller
// has already resolved the Path, authorized read Access and found a regular file.
// http.ServeContent provides Range, If-Range and HEAD handling.
func (s *server) download(w http.ResponseWriter, r *http.Request, user string, p resolver.Path, loc resolver.Location, fi fs.FileInfo) {
	f, err := loc.Open()
	if err != nil {
		s.refuseBrowse(w, r, user, p, err)
		return
	}
	defer f.Close()

	h := w.Header()
	// RFC 6266/2231: non-ASCII names are percent-encoded as filename*=utf-8''...
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": p.Name()}))
	h.Set("Cache-Control", "private, no-cache")

	cw := &countingWriter{ResponseWriter: w}
	http.ServeContent(cw, r, p.Name(), fi.ModTime(), f)

	ev := audit.Event{User: user, Operation: "download", Path: p.String(), Outcome: audit.OK, Status: cw.status, Bytes: cw.bytes}
	if cw.status >= 400 {
		ev.Outcome = audit.Refused
	}
	s.Audit.Record(ev)
}
