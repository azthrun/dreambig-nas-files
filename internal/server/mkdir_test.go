package server_test

import (
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func mkdirForm(name string) url.Values {
	return url.Values{"op": {"mkdir"}, "name": {name}}
}

func isDir(e *testenv.Env, rel string) bool {
	fi, err := os.Stat(filepath.Join(e.Dir, rel))
	return err == nil && fi.IsDir()
}

func lastAudit(t *testing.T, e *testenv.Env) map[string]any {
	t.Helper()
	ls := auditLines(t, e)
	if len(ls) == 0 {
		t.Fatal("no audit lines")
	}
	return ls[len(ls)-1]
}

func TestCreateFolderInAWritableMount(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/tax")
	c := e.SignIn(testenv.DefaultUser)
	rec := c.Post("/files/docs/tax", mkdirForm("2025"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/files/docs/tax" {
		t.Fatalf("POST = %d to %q, want 303 back to the folder", rec.Code, rec.Header().Get("Location"))
	}
	if !isDir(e, "docs/tax/2025") {
		t.Fatal("folder was not created")
	}
	a := lastAudit(t, e)
	if a["operation"] != "mkdir" || a["outcome"] != "ok" || a["path"] != "docs/tax/2025" || a["user"] != testenv.DefaultUser {
		t.Errorf("audit = %v", a)
	}
	if body := c.Get("/files/docs/tax").Body.String(); !strings.Contains(body, ">2025<") {
		t.Errorf("new folder not listed:\n%s", body)
	}
}

func TestCreateFolderInAMountFolderItself(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	if rec := e.SignIn(testenv.DefaultUser).Post("/files/docs", mkdirForm("new")); rec.Code != http.StatusSeeOther || !isDir(e, "docs/new") {
		t.Fatalf("POST = %d, dir=%v", rec.Code, isDir(e, "docs/new"))
	}
}

func TestFolderNamesAreNormalisedToNFC(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	nfd := norm.NFD.String("café")
	if rec := e.SignIn(testenv.DefaultUser).Post("/files/docs", mkdirForm(nfd)); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d", rec.Code)
	}
	ents, _ := os.ReadDir(filepath.Join(e.Dir, "docs"))
	if len(ents) != 1 || ents[0].Name() != norm.NFC.String("café") {
		var names []string
		for _, en := range ents {
			names = append(names, en.Name())
		}
		t.Errorf("stored names %q, want the NFC form", names)
	}
}

func TestInvalidFolderNamesAreRefused(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	for _, name := range []string{"", "a/b", `a\b`, "a:b", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b", "x.", "x ", "..", ".", "CON", "nul.txt", "@eaDir", "#recycle", ".DS_Store", ".nas-files-trash", ".nas-files-staging", "bad\x00", "tab\t"} {
		rec := c.Post("/files/docs", mkdirForm(name))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name %q = %d, want 400", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), html.EscapeString(i18n.T(i18n.Default, "error.bad_name"))) {
			t.Errorf("name %q: no clear message", name)
		}
		if a := lastAudit(t, e); a["operation"] != "mkdir" || a["outcome"] != "refused" || a["status"] != float64(400) {
			t.Errorf("name %q audit = %v", name, a)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(e.Dir, "docs")); len(ents) != 0 {
		t.Errorf("invalid names created %d entries", len(ents))
	}
}

func TestExistingNameGivesAClearMessage(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/Reports")
	e.WriteFile("docs/Reports/keep.txt", "k")
	e.WriteFile("docs/note.txt", "n")
	c := e.SignIn(testenv.DefaultUser)
	for _, name := range []string{"Reports", "note.txt", "reports", "REPORTS"} {
		rec := c.Post("/files/docs", mkdirForm(name))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), i18n.T(i18n.Default, "error.exists")) {
			t.Errorf("name %q = %d, want 409 with the exists message", name, rec.Code)
		}
	}
	if e.ReadFile("docs/Reports/keep.txt") != "k" || e.ReadFile("docs/note.txt") != "n" {
		t.Error("existing content changed")
	}
}

// A refused name keeps the User on the folder: the listing, and the form open on what they typed.
func TestRefusedNameKeepsTheFormOnTheFolderPage(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/Reports")
	c := e.SignIn(testenv.DefaultUser)
	body := c.Post("/files/docs", mkdirForm("reports")).Body.String()
	for _, want := range []string{`<details class="popover" open>`, `value="reports"`, `aria-invalid="true"`, ">Reports<"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestCreateFolderNeedsWriteAccessOnAWritableMount(t *testing.T) {
	e := accessEnv(t) // alice writes docs; archive is a read-only Mount; bob reads docs; carol nothing
	e.Symlink("docs/tolink", "../secret")
	alice, bob, carol := e.SignIn("alice"), e.SignIn("bob"), e.SignIn("carol")
	cases := []struct {
		who    string
		c      *testenv.Client
		target string
		name   string
		want   int
	}{
		{"read Access", bob, "/files/docs", "x", 403},
		{"read-only Mount", alice, "/files/archive", "x", 403},
		{"the Root", alice, "/files", "x", 403},
		{"the Root, an existing Mount name", alice, "/files", "docs", 403},
		{"name linking to a Mount folder", alice, "/files/docs", "tolink", 403},
		{"no Access to the Mount", carol, "/files/docs", "x", 404},
		{"Mount nobody can see", bob, "/files/secret", "x", 404},
		{"missing parent", alice, "/files/docs/nope", "x", 404},
		{"parent is a file", alice, "/files/docs/a.txt", "x", 404},
		{"hidden parent", alice, "/files/docs/@eaDir", "x", 404},
		{"escaping parent", alice, "/files/docs/%2e%2e", "x", 400},
	}
	for _, tc := range cases {
		rec := tc.c.Post(tc.target, mkdirForm(tc.name))
		if rec.Code != tc.want {
			t.Errorf("%s: POST %s = %d, want %d", tc.who, tc.target, rec.Code, tc.want)
		}
	}
	for _, rel := range []string{"docs/x", "archive/x", "x", "secret/x", "docs/nope"} {
		if _, err := os.Lstat(filepath.Join(e.Dir, rel)); err == nil {
			t.Errorf("%s was created", rel)
		}
	}
	// Refusals are audited with the User and Path.
	e.Audit.Reset()
	bob.Post("/files/docs", mkdirForm("x"))
	if a := lastAudit(t, e); a["operation"] != "mkdir" || a["outcome"] != "refused" || a["user"] != "bob" || a["status"] != float64(403) || a["path"] != "docs/x" {
		t.Errorf("audit = %v", a)
	}
}

func TestCreateFolderNeedsSessionAndCSRF(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	if rec := c.PostWithoutCSRF("/files/docs", mkdirForm("x")); rec.Code != http.StatusForbidden {
		t.Errorf("no token = %d, want 403", rec.Code)
	}
	if rec := c.PostWithToken("/files/docs", mkdirForm("x"), "wrong"); rec.Code != http.StatusForbidden {
		t.Errorf("wrong token = %d, want 403", rec.Code)
	}
	if rec := e.Anonymous().PostWithoutCSRF("/files/docs", mkdirForm("x")); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session = %d, want 401", rec.Code)
	}
	if isDir(e, "docs/x") {
		t.Error("folder created without CSRF or session")
	}
}

func TestBrowseOffersAPlainFormOnlyWhereTheUserCanWrite(t *testing.T) {
	e := accessEnv(t)
	has := func(c *testenv.Client, path string) bool {
		body := c.Get(path).Body.String()
		return strings.Contains(body, `name="op" value="mkdir"`) && strings.Contains(body, `name="name"`) && strings.Contains(body, `name="csrf_token"`) && strings.Contains(body, `method="post"`)
	}
	alice, bob := e.SignIn("alice"), e.SignIn("bob")
	if !has(alice, "/files/docs") {
		t.Error("writer sees no create-folder form")
	}
	if has(bob, "/files/docs") {
		t.Error("read-only User sees the form")
	}
	if has(alice, "/files/archive") {
		t.Error("form shown in a read-only Mount")
	}
}
