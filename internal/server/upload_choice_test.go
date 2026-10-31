package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func TestKeepBothPicksTheNextFreeName(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/report.pdf", "one")
	e.WriteFile("docs/report (1).pdf", "two")
	c := e.SignIn("alice")

	if rec := uploadWith(t, c, "docs/report.pdf", "keep", "three"); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}
	if rec := uploadWith(t, c, "docs/report.pdf", "keep", "four"); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}

	for rel, want := range map[string]string{
		"docs/report.pdf": "one", "docs/report (1).pdf": "two",
		"docs/report (2).pdf": "three", "docs/report (3).pdf": "four",
	} {
		if got := e.ReadFile(rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if l := lastAuditOf(t, e, "upload"); l == nil || l["choice"] != "keep" {
		t.Errorf("completion not audited with the choice: %v", l)
	}
}

func TestKeepBothWithoutAnExtension(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/Makefile", "one")
	uploadWith(t, e.SignIn("alice"), "docs/Makefile", "keep", "two")
	if got := e.ReadFile("docs/Makefile (1)"); got != "two" {
		t.Fatalf("got %q", got)
	}
}

func TestAnUnknownOrSkipChoiceIsRefusedAtCreation(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/a.txt", "orig")
	c := e.SignIn("alice")
	for _, choice := range []string{"skip", "overwrite", "REPLACE", "../x"} {
		if rec := tusCreateChoice(c, "docs/a.txt", choice, 3); rec.Code != http.StatusBadRequest {
			t.Errorf("choice %q = %d, want 400", choice, rec.Code)
		}
	}
	if got := e.ReadFile("docs/a.txt"); got != "orig" {
		t.Fatalf("file = %q", got)
	}
}

func TestWithoutAChoiceAnExistingFileIsStillAConflict(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/a.txt", "orig")
	rec := tusCreate(e.SignIn("alice"), "docs/a.txt", 3)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ERR_CONFLICT") ||
		strings.Contains(rec.Body.String(), "FOLDER") {
		t.Fatalf("create = %d %q", rec.Code, rec.Body)
	}
}

func TestAFolderAtThePathRefusesEveryChoice(t *testing.T) {
	e := upEnv(t)
	e.Mkdir("docs/stuff")
	e.WriteFile("docs/stuff/inner.txt", "inner")
	c := e.SignIn("alice")
	for _, choice := range []string{"", "keep", "replace"} {
		rec := tusCreateChoice(c, "docs/stuff", choice, 3)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ERR_CONFLICT_FOLDER") {
			t.Errorf("choice %q: create = %d %q", choice, rec.Code, rec.Body)
		}
	}
	if got := e.ReadFile("docs/stuff/inner.txt"); got != "inner" {
		t.Fatalf("folder content changed: %q", got)
	}
	if exists(e, "docs/"+trashDay) || exists(e, "docs/stuff (1)") {
		t.Error("a refused choice still moved or made something")
	}
}

func TestAFolderThatAppearsLateIsNotReplacedOrKeptBeside(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreateChoice(c, "docs/late", "replace", 6))
	tusPatch(c, loc, 0, "abc")
	e.Mkdir("docs/late")
	rec := tusPatch(c, loc, 3, "def")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ERR_CONFLICT_FOLDER") {
		t.Fatalf("last PATCH = %d %q", rec.Code, rec.Body)
	}
	for _, choice := range []string{"replace", "keep"} {
		if r := resolve(c, loc, choice); r.Code != http.StatusConflict {
			t.Errorf("resolve %s over a folder = %d", choice, r.Code)
		}
	}
	if exists(e, "docs/"+trashDay) || exists(e, "docs/late (1)") {
		t.Error("the folder was moved or a copy made beside it")
	}
	if r := resolve(c, loc, "skip"); r.Code != http.StatusNoContent {
		t.Errorf("resolve skip = %d", r.Code)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("skip left the staged data")
	}
}

func lateConflict(t *testing.T, e *testenv.Env, c *testenv.Client) string {
	t.Helper()
	loc := uploadURL(t, tusCreate(c, "docs/late.txt", 6))
	tusPatch(c, loc, 0, "new")
	e.WriteFile("docs/late.txt", "someone else")
	rec := tusPatch(c, loc, 3, "new")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ERR_CONFLICT") {
		t.Fatalf("last PATCH = %d %q", rec.Code, rec.Body)
	}
	return loc
}

func resolve(c *testenv.Client, loc, choice string) *httptest.ResponseRecorder {
	return c.Do(tusReq(c, http.MethodPost, loc+"/resolve?choice="+choice, "", nil))
}

func TestALateConflictIsSettledByTheUserWhileTheDataIsKept(t *testing.T) {
	for _, tc := range []struct {
		choice, auditOp string
		files           map[string]string
		trash           string
	}{
		{"keep", "upload", map[string]string{"docs/late.txt": "someone else", "docs/late (1).txt": "newnew"}, ""},
		{"replace", "upload", map[string]string{"docs/late.txt": "newnew"}, "someone else"},
		{"skip", "upload_skip", map[string]string{"docs/late.txt": "someone else"}, ""},
	} {
		t.Run(tc.choice, func(t *testing.T) {
			e := upEnv(t)
			c := e.SignIn("alice")
			loc := lateConflict(t, e, c)
			if stagingEmpty(e, "docs") {
				t.Fatal("staged data dropped before the User chose")
			}
			if rec := resolve(c, loc, tc.choice); rec.Code != http.StatusNoContent {
				t.Fatalf("resolve = %d: %s", rec.Code, rec.Body)
			}
			for rel, want := range tc.files {
				if got := e.ReadFile(rel); got != want {
					t.Errorf("%s = %q, want %q", rel, got, want)
				}
			}
			if tc.trash != "" && e.ReadFile("docs/"+trashDay+"/late.txt") != tc.trash {
				t.Error("replaced file is not in the Trash")
			}
			if tc.choice == "skip" && exists(e, "docs/late (1).txt") {
				t.Error("skip placed the file")
			}
			if !stagingEmpty(e, "docs") {
				t.Error("staged data left behind after the choice")
			}
			if l := lastAuditOf(t, e, tc.auditOp); l == nil || l["choice"] != tc.choice || l["outcome"] != "ok" {
				t.Errorf("choice not audited: %v", l)
			}
			if rec := resolve(c, loc, "keep"); rec.Code != http.StatusNotFound {
				t.Errorf("second resolve = %d", rec.Code)
			}
		})
	}
}

func TestOnlyTheCreatorCanResolveAndOnlyAnUploadThatNeedsIt(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := lateConflict(t, e, c)

	if rec := resolve(e.SignIn("bob"), loc, "replace"); rec.Code != http.StatusNotFound {
		t.Errorf("another User resolved: %d", rec.Code)
	}
	if rec := resolve(c, loc, "bogus"); rec.Code != http.StatusBadRequest {
		t.Errorf("bogus choice = %d", rec.Code)
	}
	if rec := c.Do(tusReq(c, http.MethodGet, loc+"/resolve?choice=keep", "", nil)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET resolve = %d", rec.Code)
	}
	noCSRF := httptest.NewRequest(http.MethodPost, loc+"/resolve?choice=replace", nil)
	if rec := c.Do(noCSRF); rec.Code != http.StatusForbidden {
		t.Errorf("resolve without CSRF = %d", rec.Code)
	}
	if got := e.ReadFile("docs/late.txt"); got != "someone else" {
		t.Fatalf("file = %q", got)
	}

	// An Upload still in flight has nothing to resolve.
	open := uploadURL(t, tusCreate(c, "docs/open.txt", 10))
	tusPatch(c, open, 0, "abc")
	if rec := resolve(c, open, "keep"); rec.Code != http.StatusNotFound {
		t.Errorf("resolve of an unfinished Upload = %d", rec.Code)
	}
}

func TestACancelledLateConflictDiscardsTheData(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := lateConflict(t, e, c)
	if rec := c.Do(tusReq(c, http.MethodDelete, loc, "", nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d", rec.Code)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("data left")
	}
}

func TestReplaceAndKeepNeedWriteAccess(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("archive/old.txt", "old")
	bob := e.SignIn("bob") // read on archive, which is also a read-only Mount
	for _, choice := range []string{"replace", "keep"} {
		if rec := tusCreateChoice(bob, "archive/old.txt", choice, 3); rec.Code != http.StatusForbidden {
			t.Errorf("%s with read Access = %d", choice, rec.Code)
		}
	}
	if got := e.ReadFile("archive/old.txt"); got != "old" || exists(e, "archive/"+trashDay) {
		t.Error("a refused replace touched the file")
	}

	r := testenv.New(t, testenv.Mount("docs"), testenv.WithUser("carol"), testenv.Grant("carol", "docs", "read"))
	r.WriteFile("docs/x.txt", "old")
	if rec := tusCreateChoice(r.SignIn("carol"), "docs/x.txt", "replace", 3); rec.Code != http.StatusForbidden {
		t.Errorf("replace by a reader = %d", rec.Code)
	}
	if got := r.ReadFile("docs/x.txt"); got != "old" {
		t.Error("a reader replaced a file")
	}
}

func TestAClientCannotSmuggleOtherMetadataWithTheChoice(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/a.txt", "old")
	c := e.SignIn("alice")
	rec := c.Do(tusReq(c, http.MethodPost, "/uploads/", "", map[string]string{
		"Upload-Length":   "3",
		"Upload-Metadata": tusMeta(map[string]string{"path": "docs/a.txt", "conflict": "replace", "user": "bob", "mount": "archive"}),
	}))
	loc := uploadURL(t, rec)
	if rec := tusPatch(c, loc, 0, "new"); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}
	if e.ReadFile("docs/a.txt") != "new" {
		t.Error("not placed")
	}
}
