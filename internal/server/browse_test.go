package server_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// inOrder reports whether the link texts appear in the body in the given order.
func inOrder(body string, names ...string) bool {
	pos := 0
	for _, n := range names {
		i := strings.Index(body[pos:], ">"+n+"<")
		if i < 0 {
			return false
		}
		pos += i + len(n)
	}
	return true
}

func browse(t *testing.T, e *testenv.Env, target string) *httptest.ResponseRecorder {
	t.Helper()
	return e.SignIn(testenv.DefaultUser).Get(target)
}

func TestBrowseShowsNameSizeAndModifiedTimeFoldersFirst(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/alpha.txt", "12345")
	e.Mkdir("docs/zeta")
	e.Mkdir("docs/beta")
	e.WriteFile("docs/Charlie.txt", strings.Repeat("x", 2048))
	e.Chtimes("docs/alpha.txt", time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC))

	rec := browse(t, e, "/files/docs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !inOrder(body, "beta", "zeta", "alpha.txt", "Charlie.txt") {
		t.Errorf("want folders first, then files by name:\n%s", body)
	}
	for _, want := range []string{"2025-03-04 05:06", "2.0 KB", "5 B"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, e.Dir) {
		t.Error("page leaks the absolute NAS path")
	}
}

func TestBrowseLinksToSubfoldersAndBreadcrumbs(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/tax return/2025 & more")
	c := e.SignIn(testenv.DefaultUser)

	body := c.Get("/files/docs").Body.String()
	if !strings.Contains(body, `href="/files/docs/tax%20return"`) {
		t.Errorf("no link to subfolder:\n%s", body)
	}
	body = c.Get("/files/docs/tax%20return/2025%20&%20more").Body.String()
	for _, href := range []string{`href="/"`, `href="/files/docs"`, `href="/files/docs/tax%20return"`} {
		if !strings.Contains(body, href) {
			t.Errorf("breadcrumb lacks %s:\n%s", href, body)
		}
	}
}

func TestRootPageLinksOpenTheMounts(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	if rec := c.Get("/files/docs"); rec.Code != http.StatusOK {
		t.Errorf("Mount folder = %d", rec.Code)
	}
	for _, p := range []string{"/files", "/files/"} {
		rec := c.Get(p)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
			t.Errorf("GET %s = %d %q, want redirect to /", p, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestEmptyFolderSaysSo(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/empty")
	body := browse(t, e, "/files/docs/empty").Body.String()
	if !strings.Contains(body, "This folder is empty.") {
		t.Errorf("no empty message:\n%s", body)
	}
	if strings.Contains(body, "Download selected as zip") {
		t.Error("empty folder offers a selection")
	}
}

func TestTheSortedColumnTellsAssistiveTechnologyItsDirection(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	for order, want := range map[string]string{"asc": "ascending", "desc": "descending"} {
		body := browse(t, e, "/files/docs?sort=name&order="+order).Body.String()
		if !strings.Contains(body, `aria-sort="`+want+`"`) {
			t.Errorf("order=%s: no aria-sort=%q", order, want)
		}
	}
}

func TestBrowseNeedsASession(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/secret.txt", "x")
	rec := e.Anonymous().Get("/files/docs")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("got %d %q, want redirect to /login", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(rec.Body.String(), "secret.txt") {
		t.Error("listing leaked to an anonymous client")
	}
}

func TestHiddenEntriesAreNeverShown(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/visible.txt", "x")
	for _, h := range []string{"@eaDir", "#recycle", "#snapshot", "@anything", ".nas-files-trash", ".nas-mcp-trash", ".nas-files-staging"} {
		e.Mkdir("docs/" + h)
	}
	e.WriteFile("docs/.DS_Store", "x")
	e.WriteFile("docs/sub/@eaDir/x", "x")
	e.WriteFile("docs/sub/ok.txt", "x")

	c := e.SignIn(testenv.DefaultUser)
	for _, p := range []string{"/files/docs", "/files/docs/sub"} {
		body := c.Get(p).Body.String()
		for _, h := range []string{"eaDir", "recycle", "snapshot", "anything", "nas-files-trash", "nas-mcp-trash", "staging", "DS_Store"} {
			if strings.Contains(body, h) {
				t.Errorf("%s shows Hidden Entry %q", p, h)
			}
		}
	}
	if !strings.Contains(c.Get("/files/docs").Body.String(), "visible.txt") {
		t.Error("visible entry missing")
	}
}

// Break-it tests: every one of these must be refused and must reveal nothing.
func TestPathResolverRefusesEscapesAndHiddenEntries(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.ReadOnlyMount("archive"))
	e.WriteFile("docs/ok.txt", "x")
	e.Mkdir("docs/sub")
	e.Mkdir("docs/@eaDir")
	e.Mkdir("docs/.nas-files-trash/2026-01-01")
	e.Mkdir("docs/#recycle")
	e.WriteFile("outside/secret.txt", "TOPSECRET")
	e.WriteFile("archive/.nas-mcp-trash/x", "x")
	c := e.SignIn(testenv.DefaultUser)

	bad := map[string]int{
		// syntax: 400
		"/files/docs/..":             400,
		"/files/docs/sub/../ok.txt":  400,
		"/files/docs/./sub":          400,
		"/files/docs//sub":           400,
		"/files/docs/sub/":           400,
		"/files/docs/%2e%2e":         400,
		"/files/docs/%2E%2E/outside": 400,
		"/files/docs%2F..%2Foutside": 400,
		"/files/docs/sub%2F..":       400,
		"/files/docs/sub%2f":         400,
		"/files/docs%5Csub":          400,
		"/files/docs/..%5Coutside":   400,
		"/files/docs/%00":            400,
		"/files/docs/sub%00":         400,
		"/files/docs/%0Aevil":        400,
		"/files/docs/%1F":            400,
		"/files/docs/%ff":            400,
		"/files//docs":               400,
		"/files/../etc":              400,
		"/files/%2e%2e/%2e%2e/etc":   400,
		"/files/docs/%2e":            400,
		// Hidden Entries and things that do not exist: 404, same as not-found
		"/files/docs/@eaDir":                      404,
		"/files/docs/%40eaDir":                    404,
		"/files/docs/#recycle":                    404,
		"/files/docs/%23recycle":                  404,
		"/files/docs/.nas-files-trash":            404,
		"/files/docs/.nas-files-trash/2026-01-01": 404,
		"/files/docs/.NAS-FILES-TRASH":            404,
		"/files/archive/.nas-mcp-trash":           404,
		"/files/docs/sub/@eaDir":                  404,
		"/files/nomount":                          404,
		"/files/docs/nothing":                     404,
		"/files/docs/ok.txt/more":                 404,
	}
	for target, want := range bad {
		rec := c.Get(target)
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", target, rec.Code, want)
		}
		if b := rec.Body.String(); strings.Contains(b, "TOPSECRET") || strings.Contains(b, "secret.txt") || strings.Contains(b, e.Dir) {
			t.Errorf("GET %s leaks: %s", target, b)
		}
	}
}

func TestRefusedPathsAreAudited(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/@eaDir")
	c := e.SignIn(testenv.DefaultUser)
	c.Get("/files/docs/@eaDir")
	c.Get("/files/docs/..")

	var hidden, invalid bool
	for _, l := range auditLines(t, e) {
		if l["operation"] != "browse" || l["outcome"] != "refused" {
			continue
		}
		if l["path"] == "docs/@eaDir" && l["status"] == float64(404) && l["user"] == testenv.DefaultUser {
			hidden = true
		}
		if l["path"] == nil && l["status"] == float64(400) {
			invalid = true
		}
	}
	if !hidden || !invalid {
		t.Errorf("missing refusal audit lines (hidden=%v invalid=%v):\n%s", hidden, invalid, e.Audit.String())
	}
}

func TestSymlinksCannotEscapeTheRoot(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("outside/secret.txt", "TOPSECRET")
	e.Symlink("docs/abs", filepath.Join(e.Dir, "outside"))
	e.Symlink("docs/rel", "../outside")
	e.Symlink("docs/up", "..") // the data directory itself, which is not a Mount folder
	e.Symlink("docs/etc", "/etc")
	e.Symlink("docs/dangling", "nowhere")
	e.Symlink("docs/sub/deep", "../../../outside")
	e.WriteFile("docs/ok.txt", "x")
	c := e.SignIn(testenv.DefaultUser)

	for _, p := range []string{"/files/docs/abs", "/files/docs/rel", "/files/docs/up", "/files/docs/etc",
		"/files/docs/dangling", "/files/docs/abs/secret.txt", "/files/docs/rel/secret.txt", "/files/docs/up/outside",
		"/files/docs/sub/deep"} {
		rec := c.Get(p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "secret.txt") {
			t.Errorf("GET %s leaks the outside folder", p)
		}
	}
	// The listing itself does not offer the escaping links.
	for _, p := range []string{"/files/docs", "/files/docs/sub"} {
		body := c.Get(p).Body.String()
		for _, n := range []string{"abs", "rel", "up", "etc", "dangling", "deep"} {
			if strings.Contains(body, ">"+n+"<") {
				t.Errorf("%s offers escaping symlink %q", p, n)
			}
		}
	}
	if !strings.Contains(c.Get("/files/docs").Body.String(), "ok.txt") {
		t.Error("listing lost ok.txt")
	}
}

func TestSymlinkToHiddenEntryIsRefused(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/@eaDir/meta", "x")
	e.Mkdir("docs/.nas-files-trash/2026-01-01")
	e.Symlink("docs/viaea", "@eaDir")
	e.Symlink("docs/viatrash", ".nas-files-trash")
	e.Symlink("docs/self", ".") // symlink to the Mount folder, then a hidden name under it
	c := e.SignIn(testenv.DefaultUser)

	for _, p := range []string{"/files/docs/viaea", "/files/docs/viatrash", "/files/docs/self/.nas-files-trash", "/files/docs/self/@eaDir"} {
		if rec := c.Get(p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	body := c.Get("/files/docs").Body.String()
	for _, n := range []string{"viaea", "viatrash"} {
		if strings.Contains(body, ">"+n+"<") {
			t.Errorf("listing offers symlink %q to a Hidden Entry", n)
		}
	}
}

func TestSymlinkBetweenMountsIsJudgedByTheRealTargetsMount(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.ReadOnlyMount("archive"))
	e.WriteFile("archive/old/report.txt", "x")
	e.Symlink("docs/shortcut", filepath.Join(e.Dir, "archive", "old"))
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Get("/files/docs/shortcut")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "report.txt") {
		t.Fatalf("link into another Mount = %d, want its listing", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), i18n.T(i18n.Default, "root.read_only")) {
		t.Error("page does not show the target Mount (archive) as read-only")
	}
	if strings.Contains(c.Get("/files/docs").Body.String(), i18n.T(i18n.Default, "root.read_only")) {
		t.Error("writable Mount shown as read-only")
	}
}

func TestSortByNameSizeAndModifiedWithFoldersFirst(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "12345")
	e.WriteFile("docs/b.txt", "1")
	e.WriteFile("docs/c.txt", "123")
	e.Mkdir("docs/dir")
	e.Chtimes("docs/a.txt", testenv.Start.Add(-1*time.Hour))
	e.Chtimes("docs/b.txt", testenv.Start.Add(-3*time.Hour))
	e.Chtimes("docs/c.txt", testenv.Start.Add(-2*time.Hour))
	c := e.SignIn(testenv.DefaultUser)

	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"dir", "a.txt", "b.txt", "c.txt"}},
		{"?sort=name&order=desc", []string{"dir", "c.txt", "b.txt", "a.txt"}},
		{"?sort=size", []string{"dir", "b.txt", "c.txt", "a.txt"}},
		{"?sort=size&order=desc", []string{"dir", "a.txt", "c.txt", "b.txt"}},
		{"?sort=modified", []string{"dir", "b.txt", "c.txt", "a.txt"}},
		{"?sort=modified&order=desc", []string{"dir", "a.txt", "c.txt", "b.txt"}},
		{"?sort=bogus&order=sideways", []string{"dir", "a.txt", "b.txt", "c.txt"}},
	}
	for _, tc := range cases {
		rec := c.Get("/files/docs" + tc.query)
		if rec.Code != http.StatusOK || !inOrder(rec.Body.String(), tc.want...) {
			t.Errorf("%q: status %d, want order %v\n%s", tc.query, rec.Code, tc.want, rec.Body.String())
		}
	}
	body := c.Get("/files/docs").Body.String()
	for _, key := range []string{"sort=name", "sort=size", "sort=modified"} {
		if !strings.Contains(body, key) {
			t.Errorf("no sort link for %s", key)
		}
	}
}

func TestSortLinksDoNotLoseThePath(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a b/x.txt", "x")
	body := browse(t, e, "/files/docs/a%20b").Body.String()
	if !regexp.MustCompile(`href="/files/docs/a%20b\?sort=size`).MatchString(body) {
		t.Errorf("sort link lacks the folder:\n%s", body)
	}
}

func TestNonASCIINamesRenderAndOpen(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/文档/报告 – 终稿.txt", "x")
	e.WriteFile("docs/café/naïve.txt", "x")
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Get("/files/docs")
	body := rec.Body.String()
	if !strings.Contains(body, ">文档<") || !strings.Contains(body, ">café<") {
		t.Errorf("names garbled:\n%s", body)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "utf-8") {
		t.Errorf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
	if rec := c.Get("/files/docs/%E6%96%87%E6%A1%A3"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "报告 – 终稿.txt") {
		t.Errorf("CJK folder = %d", rec.Code)
	}
	// A client that sends the decomposed form (macOS) reaches the same folder.
	if rec := c.Get("/files/docs/cafe%CC%81"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "naïve.txt") {
		t.Errorf("NFD URL = %d", rec.Code)
	}
}

func TestHugeFolderIsPaged(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	for i := 0; i < 3000; i++ {
		if err := os.WriteFile(filepath.Join(e.Dir, "docs", fmt.Sprintf("file-%05d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := e.SignIn(testenv.DefaultUser)

	body := c.Get("/files/docs").Body.String()
	rows := strings.Count(body, "<tr")
	if rows > 700 || rows < 100 {
		t.Errorf("first page has %d rows, want a bounded page", rows)
	}
	if !strings.Contains(body, "file-00000.txt") || strings.Contains(body, "file-02999.txt") {
		t.Error("first page content wrong")
	}
	if !strings.Contains(body, "page=2") {
		t.Fatal("no link to the next page")
	}
	if !strings.Contains(c.Get("/files/docs?page=6").Body.String(), "file-02999.txt") {
		t.Error("last entry unreachable")
	}
	if rec := c.Get("/files/docs?page=999"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "file-02999.txt") {
		t.Errorf("out-of-range page = %d, want the last page", rec.Code)
	}
	if rec := c.Get("/files/docs?page=abc"); rec.Code != 200 {
		t.Errorf("bad page = %d", rec.Code)
	}
	// Paging keeps the chosen sort.
	if !strings.Contains(c.Get("/files/docs?sort=name&order=desc").Body.String(), "sort=name&amp;order=desc&amp;page=2") {
		t.Error("next-page link loses the sort")
	}
}

func TestPageWorksWithoutJavaScriptAndHasResponsiveDarkStyles(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "x")
	body := browse(t, e, "/files/docs").Body.String()
	// Scripts only enhance (the Upload panel, #11): never inline, and the listing itself
	// is server-rendered.
	if strings.Contains(strings.ToLower(body), "<script>") || !strings.Contains(body, "a.txt") {
		t.Error("page needs JavaScript")
	}
	for _, want := range []string{`name="viewport"`, "/static/app.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	css := e.Anonymous().Get("/static/app.css").Body.String()
	if !strings.Contains(css, "prefers-color-scheme: dark") || !strings.Contains(css, "@media (max-width") {
		t.Error("stylesheet lacks dark mode or responsive rules")
	}
}
