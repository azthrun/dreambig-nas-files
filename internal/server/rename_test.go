package server_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func askRename(names ...string) url.Values {
	return url.Values{"names": names, "op": {"rename"}}
}

func doRename(old, name string) url.Values {
	return url.Values{"names": {old}, "name": {name}, "op": {"rename-confirm"}}
}

func auditedOK(t *testing.T, e *testenv.Env, op string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range auditLines(t, e) {
		if l["operation"] == op && l["outcome"] == "ok" {
			out = append(out, l)
		}
	}
	return out
}

func TestRenameAsksForTheNameAndChangesNothing(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Post(folderURL("docs"), askRename("a.txt"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="op" value="rename-confirm"`, `name="names" value="a.txt"`, `name="name" value="a.txt"`, `name="csrf_token"`, `action="/files/docs"`} {
		if !strings.Contains(body, want) {
			t.Errorf("rename page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("rename must work without JavaScript")
	}
	if !exists(e, "docs/a.txt") {
		t.Error("asking must not change anything")
	}
}

func TestRenamingAFileAndAFolderKeepsContentAndAudits(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("docs/old/inside.txt", "inside")
	c := e.SignIn(testenv.DefaultUser)

	for _, step := range [][2]string{{"a.txt", "税单.txt"}, {"old", "new"}} {
		rec := c.Post(folderURL("docs"), doRename(step[0], step[1]))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/files/docs" {
			t.Fatalf("%v: status %d, Location %q", step, rec.Code, rec.Header().Get("Location"))
		}
	}
	if exists(e, "docs/a.txt") || e.ReadFile("docs/税单.txt") != "a" {
		t.Error("the file was not renamed")
	}
	if exists(e, "docs/old") || e.ReadFile("docs/new/inside.txt") != "inside" {
		t.Error("the folder was not renamed")
	}
	lines := auditedOK(t, e, "rename")
	if len(lines) != 2 || lines[1]["path"] != "docs/new" || lines[1]["from"] != "docs/old" {
		t.Errorf("audit = %v", lines)
	}
}

func TestRenameOnlyChangingCaseIsAllowed(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/foo.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.Post(folderURL("docs"), doRename("foo.txt", "Foo.txt")); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	names, err := os.ReadDir(filepath.Join(e.Dir, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0].Name() != "Foo.txt" {
		t.Errorf("folder holds %v", names)
	}
}

func TestRenameToTheSameNameChangesNothing(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.Post(folderURL("docs"), doRename("a.txt", "a.txt")); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if len(auditedOK(t, e, "rename")) != 0 {
		t.Error("nothing happened, so nothing should be audited")
	}
}

func TestRenameNeverReplacesAndKeepsThePageOpen(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("docs/b.txt", "b")
	e.Mkdir("docs/folder")
	e.Symlink("docs/dangling", "nowhere")
	c := e.SignIn(testenv.DefaultUser)

	for _, name := range []string{"b.txt", "B.TXT", "folder", "dangling"} {
		rec := c.Post(folderURL("docs"), doRename("a.txt", name))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: status %d, want 409", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `value="rename-confirm"`) {
			t.Errorf("%s: the rename page should stay open:\n%s", name, rec.Body)
		}
	}
	if e.ReadFile("docs/a.txt") != "a" || e.ReadFile("docs/b.txt") != "b" {
		t.Error("a refused rename changed something")
	}
}

func TestRenameAppliesTheNameRules(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	for _, name := range []string{"", ".", "..", "sub/b", "a:b", "b.", "nul.txt", "@eaDir", ".nas-files-trash", ".DS_Store", strings.Repeat("x", 256)} {
		if rec := c.Post(folderURL("docs"), doRename("a.txt", name)); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", name, rec.Code)
		}
	}
	if !exists(e, "docs/a.txt") {
		t.Error("a refused rename changed something")
	}
}

func TestRenameTakesExactlyOneItem(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("docs/b.txt", "b")
	c := e.SignIn(testenv.DefaultUser)

	for _, v := range []url.Values{askRename(), askRename("a.txt", "b.txt"), {"names": {"a.txt", "b.txt"}, "name": {"c.txt"}, "op": {"rename-confirm"}}} {
		if rec := c.Post(folderURL("docs"), v); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d", v, rec.Code)
		}
	}
	if exists(e, "docs/c.txt") {
		t.Error("something was renamed")
	}
}

func TestRenamedSymlinkIsTheLinkNeverItsTarget(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("other"))
	e.WriteFile("other/target.txt", "other")
	e.Symlink("docs/link", "../other/target.txt")
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.Post(folderURL("docs"), doRename("link", "renamed")); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if fi, err := os.Lstat(filepath.Join(e.Dir, "docs/renamed")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link itself should carry the new name: %v", err)
	}
	if e.ReadFile("other/target.txt") != "other" || !exists(e, "other/target.txt") {
		t.Error("the target changed")
	}
}

func TestRenameIsRefusedWithoutWriteAccessAndAtTheTopLevel(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.ReadOnlyMount("archive"), testenv.Mount("reading"), testenv.Mount("secret"),
		testenv.Grant(testenv.DefaultUser, "docs", "write"),
		testenv.Grant(testenv.DefaultUser, "archive", "write"),
		testenv.Grant(testenv.DefaultUser, "reading", "read"),
	)
	for _, m := range []string{"archive", "reading", "secret"} {
		e.WriteFile(m+"/a.txt", "a")
	}
	e.Symlink("docs/into-archive", "../archive")
	c := e.SignIn(testenv.DefaultUser)

	want := map[string]int{"archive": 403, "reading": 403, "secret": 404, "docs/into-archive": 403}
	for folder, code := range want {
		for _, v := range []url.Values{askRename("a.txt"), doRename("a.txt", "b.txt")} {
			if rec := c.Post(folderURL(folder), v); rec.Code != code {
				t.Errorf("%s %s: status %d, want %d", folder, v.Get("op"), rec.Code, code)
			}
		}
	}
	if rec := c.Post(folderURL(""), doRename("docs", "renamed")); rec.Code != http.StatusForbidden {
		t.Errorf("renaming a Mount: status %d", rec.Code)
	}
	for _, m := range []string{"archive", "reading", "secret"} {
		if !exists(e, m+"/a.txt") || exists(e, m+"/b.txt") {
			t.Errorf("%s changed", m)
		}
	}
}

func TestRenameNeedsACSRFTokenAndASession(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	v := doRename("a.txt", "b.txt")
	if rec := c.PostWithoutCSRF("/files/docs", v); rec.Code != http.StatusForbidden {
		t.Errorf("without token = %d", rec.Code)
	}
	if rec := e.Anonymous().PostWithoutCSRF("/files/docs", v); rec.Code != http.StatusUnauthorized {
		t.Errorf("without session = %d", rec.Code)
	}
	if !exists(e, "docs/a.txt") {
		t.Error("a refused request renamed the file")
	}
}

func TestRenameRefusesHiddenEntriesAsMissing(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/.nas-files-trash/2026-01-01/old.txt", "old")
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.Post(folderURL("docs"), doRename(".nas-files-trash", "x")); rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
	if rec := c.Post(folderURL("docs/.nas-files-trash/2026-01-01"), doRename("old.txt", "x")); rec.Code != http.StatusNotFound {
		t.Errorf("inside the Trash: status %d", rec.Code)
	}
}

func TestBrowseOffersRenameAndMoveToWritersOnly(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("reading"),
		testenv.Grant(testenv.DefaultUser, "docs", "write"),
		testenv.Grant(testenv.DefaultUser, "reading", "read"))
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("reading/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	body := c.Get("/files/docs").Body.String()
	for _, want := range []string{`name="op" value="rename"`, `name="op" value="move"`} {
		if !strings.Contains(body, want) {
			t.Errorf("writer's listing lacks %q", want)
		}
	}
	body = c.Get("/files/reading").Body.String()
	if strings.Contains(body, `value="rename"`) || strings.Contains(body, `value="move"`) {
		t.Error("a reader is offered rename or move")
	}
}
