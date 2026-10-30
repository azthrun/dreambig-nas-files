package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/i18n"
	"github.com/azthrun/dreambig-nas-files/internal/listing"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

const filesPrefix = "/files"

type crumb struct {
	Name string
	Href string
}

type entryView struct {
	Name     string
	Href     string // folders link to their listing; files have none until download (#7)
	IsDir    bool
	Size     string
	Modified string
	ISO      string
}

type columnView struct {
	Label   string
	Href    string
	Current bool
	Arrow   string
}

type browseView struct {
	Crumbs   []crumb
	Columns  []columnView
	Entries  []entryView
	ReadOnly bool
	Empty    bool
	Page     int
	Pages    int
	PrevHref string
	NextHref string
}

// pathFromRequest extracts the Path from /files/<escaped Path>. Anything that is not
// exactly that shape is refused as an invalid Path.
func pathFromRequest(r *http.Request) (resolver.Path, error) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), filesPrefix)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return resolver.Path{}, resolver.ErrNotFound
	}
	return resolver.ParseEscaped(strings.TrimPrefix(rest, "/"))
}

// browse lists a folder: GET /files/<Path>?sort=&order=&page=.
func (s *server) browse(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	rest := strings.TrimPrefix(r.URL.EscapedPath(), filesPrefix)
	if rest == "" || rest == "/" { // the Root is the home page
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p, err := pathFromRequest(r)
	var loc resolver.Location
	if err == nil {
		loc, err = s.res.Resolve(p)
	}
	var entries []resolver.Entry
	if err == nil {
		// #5: judge Access by loc.Mount here (a User with no Access gets 404).
		if _, err = loc.Stat(); err == nil {
			entries, err = loc.ReadDir()
		}
	}
	if err != nil {
		s.refuseBrowse(w, r, user, p, err)
		return
	}

	q := r.URL.Query()
	key, order := listing.ParseKey(q.Get("sort")), listing.ParseOrder(q.Get("order"))
	listing.Sort(entries, key, order)
	n, _ := strconv.Atoi(q.Get("page"))
	shown, cur, pages := listing.Page(entries, n)

	v := &browseView{
		Crumbs:   crumbsFor(p),
		ReadOnly: loc.Mount.ReadOnly,
		Empty:    len(entries) == 0,
		Page:     cur,
		Pages:    pages,
	}
	for _, e := range shown {
		ev := entryView{Name: e.Name, IsDir: e.IsDir, Modified: e.ModTime.UTC().Format("2006-01-02 15:04"), ISO: e.ModTime.UTC().Format("2006-01-02T15:04:05Z")}
		if e.IsDir {
			child, err := p.Join(e.Name)
			if err == nil {
				ev.Href = folderHref(child)
			}
			ev.Size = "-"
		} else {
			ev.Size = humanSize(e.Size)
		}
		v.Entries = append(v.Entries, ev)
	}
	base := folderHref(p)
	for _, c := range []struct {
		key   listing.Key
		label string
	}{{listing.ByName, "browse.name"}, {listing.BySize, "browse.size"}, {listing.ByModified, "browse.modified"}} {
		next := listing.Asc
		cv := columnView{Label: c.label}
		if c.key == key {
			cv.Current = true
			cv.Arrow = "▲"
			if order == listing.Asc {
				next = listing.Desc
			} else {
				cv.Arrow = "▼"
			}
		}
		cv.Href = query(base, c.key, next, 0)
		v.Columns = append(v.Columns, cv)
	}
	if cur > 1 {
		v.PrevHref = query(base, key, order, cur-1)
	}
	if cur < pages {
		v.NextHref = query(base, key, order, cur+1)
	}
	title := p.Name()
	s.render(w, r, http.StatusOK, "browse.html", page{Title: title, Browse: v})
}

func (s *server) refuseBrowse(w http.ResponseWriter, r *http.Request, user string, p resolver.Path, err error) {
	status := resolver.Status(err)
	if status == http.StatusInternalServerError {
		s.Log.Error("browse", "err", err)
		s.errorPage(w, r, status, "error.internal")
		return
	}
	ev := audit.Event{User: user, Operation: "browse", Outcome: audit.Refused, Status: status}
	if !errors.Is(err, resolver.ErrInvalidPath) {
		ev.Path = p.String() // only a syntactically valid Path is ever recorded
	}
	// Plain not-found is routine and not audited; refusals (invalid, hidden, escape) are.
	if !(errors.Is(err, resolver.ErrNotFound) || errors.Is(err, resolver.ErrNotFolder)) {
		s.audit.Record(ev)
	}
	key := "error.not_found"
	if status == http.StatusBadRequest {
		key = "error.bad_path"
	}
	s.errorPage(w, r, status, key)
}

// crumbsFor builds the breadcrumb trail: Files > mount > folder > ... The last crumb is
// the current folder.
func crumbsFor(p resolver.Path) []crumb {
	cs := []crumb{{Name: i18n.T(i18n.Default, "root.title"), Href: "/"}}
	segs := p.Segments()
	for i, name := range segs {
		sub, err := resolver.ParseSegments(segs[:i+1])
		if err != nil {
			break
		}
		cs = append(cs, crumb{Name: name, Href: folderHref(sub)})
	}
	return cs
}

func folderHref(p resolver.Path) string {
	return filesPrefix + "/" + p.Escaped()
}

func query(base string, key listing.Key, order listing.Order, page int) string {
	// Fixed parameter order keeps the URL stable for bookmarks.
	parts := []string{"sort=" + string(key), "order=" + string(order)}
	if page > 1 {
		parts = append(parts, "page="+strconv.Itoa(page))
	}
	return base + "?" + strings.Join(parts, "&")
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f, suffix := float64(n), ""
	for _, suffix = range []string{"KB", "MB", "GB", "TB"} {
		f /= unit
		if f < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", f, suffix)
}
