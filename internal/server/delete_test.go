package server_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

const trashDay = ".nas-files-trash/2026-01-15"

// The selection form posts to the folder's own URL; op picks the step.
func folderURL(folder string) string {
	if folder == "" {
		return "/files"
	}
	return "/files/" + folder
}

func selectDelete(names ...string) url.Values {
	return url.Values{"names": names, "op": {"delete"}}
}

func doDelete(names ...string) url.Values {
	return url.Values{"names": names, "op": {"delete-confirm"}}
}

func exists(e *testenv.Env, rel string) bool {
	_, err := os.Lstat(filepath.Join(e.Dir, filepath.FromSlash(rel)))
	return err == nil
}

func TestSelectingDeleteShowsAConfirmPageNamingTheItemsAndDeletesNothing(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	e.Mkdir("docs/old stuff")
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Post(folderURL("docs"), selectDelete("a.txt", "old stuff"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{">a.txt<", ">old stuff<", `action="/files/docs"`, `name="op" value="delete-confirm"`, `name="names" value="a.txt"`, `name="csrf_token"`} {
		if !strings.Contains(body, want) {
			t.Errorf("confirm page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("confirmation must work without JavaScript")
	}
	if !exists(e, "docs/a.txt") || !exists(e, "docs/old stuff") {
		t.Error("selecting must not delete anything")
	}
}

func TestConfirmedDeleteMovesItemsToTheMountTrashAndAuditsThem(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/tax/a.txt", "a")
	e.WriteFile("docs/tax/b.txt", "b")
	e.WriteFile("docs/tax/keep.txt", "keep")
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Post(folderURL("docs/tax"), doDelete("a.txt", "b.txt"))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/files/docs/tax" {
		t.Fatalf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if exists(e, "docs/tax/a.txt") || exists(e, "docs/tax/b.txt") {
		t.Error("items are still in place")
	}
	if e.ReadFile("docs/"+trashDay+"/tax/a.txt") != "a" || e.ReadFile("docs/"+trashDay+"/tax/b.txt") != "b" {
		t.Error("items are not in the Trash")
	}
	if !exists(e, "docs/tax/keep.txt") {
		t.Error("an unselected item was deleted")
	}
	var paths []string
	for _, l := range auditLines(t, e) {
		if l["operation"] == "delete" && l["outcome"] == "ok" && l["user"] == testenv.DefaultUser {
			paths = append(paths, l["path"].(string))
		}
	}
	if strings.Join(paths, ",") != "docs/tax/a.txt,docs/tax/b.txt" {
		t.Errorf("audited deletes = %v", paths)
	}
}

func TestDeletingTheSameNameTwiceKeepsBothInTheTrash(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	e.WriteFile("docs/a.txt", "first")
	c.Post(folderURL("docs"), doDelete("a.txt"))
	e.WriteFile("docs/a.txt", "second")
	c.Post(folderURL("docs"), doDelete("a.txt"))

	if e.ReadFile("docs/"+trashDay+"/a.txt") != "first" || e.ReadFile("docs/"+trashDay+"/a (2).txt") != "second" {
		t.Error("a Trash name clash lost data")
	}
}

func TestDeletingASymlinkRemovesTheLinkNeverItsTarget(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("other"))
	e.WriteFile("docs/real/inside.txt", "inside")
	e.WriteFile("other/target.txt", "other")
	e.Symlink("docs/to-folder", "real")
	e.Symlink("docs/to-other", "../other")
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Post(folderURL("docs"), doDelete("to-folder", "to-other"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if exists(e, "docs/to-folder") || exists(e, "docs/to-other") {
		t.Error("links are still in place")
	}
	if e.ReadFile("docs/real/inside.txt") != "inside" || e.ReadFile("other/target.txt") != "other" {
		t.Error("a link target was deleted")
	}
	if fi, err := os.Lstat(filepath.Join(e.Dir, "docs", filepath.FromSlash(trashDay), "to-other")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link itself should be in the Trash: %v", err)
	}
}

func TestMountsThemselvesCannotBeDeleted(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	for _, folder := range []string{"", "docs/.."} {
		rec := c.Post(folderURL(folder), doDelete("docs"))
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
			t.Errorf("folder %q: status %d", folder, rec.Code)
		}
	}
	if rec := c.Post(folderURL(""), selectDelete("docs")); rec.Code != http.StatusForbidden {
		t.Errorf("selecting a Mount to delete: status %d", rec.Code)
	}
	if !exists(e, "docs/a.txt") {
		t.Error("the Mount lost content")
	}
	if exists(e, ".nas-files-trash") {
		t.Error("something was trashed at the top level")
	}
}

func TestDeleteIsRefusedWithoutWriteAccess(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.ReadOnlyMount("archive"), testenv.Mount("reading"), testenv.Mount("secret"),
		testenv.Grant(testenv.DefaultUser, "docs", "write"),
		testenv.Grant(testenv.DefaultUser, "archive", "write"),
		testenv.Grant(testenv.DefaultUser, "reading", "read"),
	)
	for _, m := range []string{"archive", "reading", "secret"} {
		e.WriteFile(m+"/a.txt", "a")
	}
	e.Symlink("archive/link", "a.txt")
	c := e.SignIn(testenv.DefaultUser)

	want := map[string]int{"archive": 403, "reading": 403, "secret": 404}
	for m, code := range want {
		for _, rec := range []*struct{ code int }{
			{c.Post(folderURL(m), doDelete("a.txt")).Code},
			{c.Post(folderURL(m), selectDelete("a.txt")).Code},
		} {
			if rec.code != code {
				t.Errorf("%s: status %d, want %d", m, rec.code, code)
			}
		}
		if !exists(e, m+"/a.txt") {
			t.Errorf("%s/a.txt was deleted", m)
		}
	}
	if rec := c.Post(folderURL("archive"), doDelete("link")); rec.Code != http.StatusForbidden || !exists(e, "archive/link") {
		t.Errorf("a link in a read-only Mount was deleted (status %d)", rec.Code)
	}
	if exists(e, "archive/.nas-files-trash") || exists(e, "reading/.nas-files-trash") {
		t.Error("a refused delete still made a Trash")
	}
	refused := 0
	for _, l := range auditLines(t, e) {
		if l["operation"] == "delete" && l["outcome"] == "refused" {
			refused++
		}
	}
	if refused == 0 {
		t.Error("refusals are not audited")
	}
}

func TestDeleteThroughASymlinkedFolderIsJudgedByTheRealMount(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.ReadOnlyMount("archive"))
	e.WriteFile("archive/old.txt", "old")
	e.Symlink("docs/into-archive", "../archive")
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.Post(folderURL("docs/into-archive"), doDelete("old.txt")); rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}
	if !exists(e, "archive/old.txt") || exists(e, "archive/.nas-files-trash") {
		t.Error("a read-only Mount was changed through a link")
	}
}

func TestDeleteNeedsACSRFTokenAndASession(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	for _, v := range []url.Values{selectDelete("a.txt"), doDelete("a.txt")} {
		target := "/files/docs"
		if rec := c.PostWithoutCSRF(target, v); rec.Code != http.StatusForbidden {
			t.Errorf("%s without token = %d", target, rec.Code)
		}
		if rec := c.PostWithToken(target, v, "wrong"); rec.Code != http.StatusForbidden {
			t.Errorf("%s with wrong token = %d", target, rec.Code)
		}
		if rec := e.Anonymous().PostWithoutCSRF(target, v); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without session = %d", target, rec.Code)
		}
	}
	if !exists(e, "docs/a.txt") {
		t.Error("a refused request deleted the file")
	}
}

func TestDeleteRefusesBadNamesAndHiddenEntriesAndChangesNothingWhenOneIsBad(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("docs/.nas-files-trash/2026-01-01/old.txt", "old")
	e.WriteFile("docs/@eaDir/x", "x")
	c := e.SignIn(testenv.DefaultUser)

	cases := map[string]int{
		"../a.txt":         400,
		"sub/a.txt":        400,
		"..":               400,
		".":                400,
		"":                 400,
		"a\\b":             400,
		".nas-files-trash": 404,
		"@eaDir":           404,
		"missing.txt":      404,
	}
	for name, code := range cases {
		rec := c.Post(folderURL("docs"), doDelete("a.txt", name))
		if rec.Code != code {
			t.Errorf("name %q: status %d, want %d", name, rec.Code, code)
		}
	}
	if rec := c.Post(folderURL("docs/../.."), doDelete("a.txt")); rec.Code != http.StatusBadRequest {
		t.Errorf("bad folder: status %d", rec.Code)
	}
	if rec := c.Post(folderURL("docs/.nas-files-trash/2026-01-01"), doDelete("old.txt")); rec.Code != http.StatusNotFound {
		t.Errorf("deleting inside the Trash: status %d", rec.Code)
	}
	if !exists(e, "docs/a.txt") || e.ReadFile("docs/.nas-files-trash/2026-01-01/old.txt") != "old" || !exists(e, "docs/@eaDir/x") {
		t.Error("a refused request changed something")
	}
	if rec := c.Post("/files/docs", url.Values{"op": {"delete-confirm"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("nothing selected: status %d", rec.Code)
	}
	if rec := c.Post("/files/docs", url.Values{"op": {"delete"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("nothing selected (select): status %d", rec.Code)
	}
	if rec := c.Post("/files/docs", url.Values{"names": {"a.txt"}, "op": {"nope"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action: status %d", rec.Code)
	}
}

func TestDeletedItemsStayHiddenAndUnreachableAndArePurgedAfterTheRetention(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.TrashRetention(30))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)
	c.Post(folderURL("docs"), doDelete("a.txt"))

	if body := c.Get("/files/docs").Body.String(); strings.Contains(body, "nas-files-trash") {
		t.Error("the Trash is listed")
	}
	if rec := c.Get("/files/docs/.nas-files-trash"); rec.Code != http.StatusNotFound {
		t.Errorf("Trash reachable: %d", rec.Code)
	}

	e.Clock.Advance(30 * 24 * time.Hour)
	e.PurgeTrash()
	if !exists(e, "docs/"+trashDay+"/a.txt") {
		t.Error("purged too early")
	}
	e.Clock.Advance(24 * time.Hour)
	e.PurgeTrash()
	if exists(e, "docs/"+trashDay) {
		t.Error("not purged after the retention")
	}
}

func TestBrowseOffersSelectionToWritersOnly(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("reading"),
		testenv.Grant(testenv.DefaultUser, "docs", "write"),
		testenv.Grant(testenv.DefaultUser, "reading", "read"))
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("reading/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	body := c.Get("/files/docs").Body.String()
	for _, want := range []string{`<form method="post" action="/files/docs"`, `type="checkbox" name="names" value="a.txt"`, `name="op" value="delete"`, `name="csrf_token"`} {
		if !strings.Contains(body, want) {
			t.Errorf("writer's listing lacks %q:\n%s", want, body)
		}
	}
	body = c.Get("/files/reading").Body.String()
	if strings.Contains(body, `value="delete"`) {
		t.Errorf("a reader is offered Delete:\n%s", body)
	}
}
