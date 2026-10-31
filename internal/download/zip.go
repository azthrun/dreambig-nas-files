// Package download is the streaming core of downloads: it walks folders and writes them
// as one store-only zip, judging every entry on its own. It knows nothing of HTTP; the
// server keeps the headers, the Access check and the audit line.
package download

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// maxDepth bounds the walk as a second guard next to the loop check.
const maxDepth = 64

// IncompleteName is the note added to a zip that had to leave entries out.
const IncompleteName = "_incomplete.txt"

// Item is one top-level thing to archive, under the name it gets in the zip.
type Item struct {
	Name string
	Path resolver.Path
	Loc  resolver.Location
}

// Zip writes items to w as a zip while walking: store-only, no temp files. ctx ends the
// work when the client goes away. Allow is asked about every entry found under a folder
// (the item roots are the caller's business) and judges the REAL target after symlinks;
// entries that are refused, Hidden or invalid are silently left out.
//
// Entries that fail to read (vanished, unreadable, neither a file nor a folder) are left
// out too, but listed: Zip then ends with a note (IncompleteName, or a free variant of
// it), whose text is note followed by one Path per line. It returns how many entries were
// left out for that reason.
func Zip(ctx context.Context, w io.Writer, res *resolver.Resolver, allow func(resolver.Location) bool, now time.Time, note string, items []Item) (omitted int, err error) {
	z := &walk{ctx: ctx, res: res, allow: allow, zw: zip.NewWriter(w)}
	for _, it := range items {
		if err = z.add(it.Path, it.Loc, it.Name, nil); err != nil {
			return len(z.missed), err
		}
	}
	if len(z.missed) > 0 {
		hdr := &zip.FileHeader{Name: z.noteName(items), Method: zip.Store, Modified: now}
		f, err := z.zw.CreateHeader(hdr)
		if err != nil {
			return len(z.missed), err
		}
		text := note + "\n"
		for _, m := range z.missed {
			text += m + "\n"
		}
		if _, err := io.WriteString(f, text); err != nil {
			return len(z.missed), err
		}
	}
	return len(z.missed), z.zw.Close()
}

type walk struct {
	ctx   context.Context
	res   *resolver.Resolver
	allow func(resolver.Location) bool
	zw    *zip.Writer
	// missed holds the Paths that failed to read. Hidden and no-Access entries are
	// excluded silently and never land here.
	missed []string
}

// add archives the thing at p/loc as name, descending into folders. chain holds the real
// folders being walked (Mount + location), so a symlink back into one of them is
// skipped instead of followed forever.
func (z *walk) add(p resolver.Path, loc resolver.Location, name string, chain []string) error {
	if err := z.ctx.Err(); err != nil {
		return err
	}
	fi, err := loc.Stat()
	if err != nil {
		z.miss(p) // vanished meanwhile: leave it out, and say so
		return nil
	}
	if fi.IsDir() {
		key := loc.Mount.Name + "\x00" + loc.Rel()
		for _, k := range chain {
			if k == key {
				return nil // a symlink loop
			}
		}
		if len(chain) >= maxDepth {
			z.miss(p) // too deep to follow: left out, and say so
			return nil
		}
		hdr := &zip.FileHeader{Name: name + "/", Method: zip.Store, Modified: fi.ModTime()}
		hdr.SetMode(fi.Mode())
		if _, err := z.zw.CreateHeader(hdr); err != nil {
			return err
		}
		entries, err := loc.ReadDir()
		if err != nil {
			z.miss(p)
			return nil
		}
		chain = append(chain[:len(chain):len(chain)], key)
		for _, e := range entries {
			child, err := p.Join(e.Name)
			if err != nil {
				continue
			}
			// Each entry is judged on its own: the resolver follows its symlinks to the
			// real target (Hidden, outside the Root), allow checks that target's Mount.
			cl, err := z.res.Resolve(child)
			if errors.Is(err, resolver.ErrHidden) || errors.Is(err, resolver.ErrOutsideRoot) {
				continue // excluded on purpose, silently
			}
			if err != nil || !cl.Exists {
				z.miss(child) // gone since the listing, or unreadable: say so
				continue
			}
			if !z.allow(cl) {
				continue
			}
			if err := z.add(child, cl, name+"/"+e.Name, chain); err != nil {
				return err
			}
		}
		return nil
	}
	f, err := loc.Open() // refuses a FIFO or a device, which then counts as missed
	if err != nil {
		z.miss(p)
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

// noteName is IncompleteName, or the first numbered variant no top-level item uses, so
// the note never doubles or replaces a selected file of that name.
func (z *walk) noteName(items []Item) string {
	taken := map[string]bool{}
	for _, it := range items {
		taken[it.Name] = true
	}
	name := IncompleteName
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("_incomplete (%d).txt", i)
	}
	return name
}

func (z *walk) miss(p resolver.Path) { z.missed = append(z.missed, p.String()) }

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
