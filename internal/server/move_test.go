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

func pick(at string, names ...string) url.Values {
	v := url.Values{"names": names, "op": {"move"}}
	if at != "" {
		v.Set("at", at)
	}
	return v
}

func doMove(dest string, names ...string) url.Values {
	return url.Values{"names": names, "dest": {dest}, "op": {"move-confirm"}}
}

func TestPickerWalksFoldersOfTheMountAndChangesNothing(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("other"))
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("docs/box/inner/x.txt", "x")
	e.Mkdir("docs/moving")
	e.Mkdir("docs/@eaDir")
	e.Symlink("docs/to-other", "../other")
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Post(folderURL("docs"), pick("", "a.txt", "moving"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="at" value="docs/box"`, `name="op" value="move-confirm"`, `name="names" value="a.txt"`, `name="csrf_token"`} {
		if !strings.Contains(body, want) {
			t.Errorf("picker lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{`value="docs/moving"`, `eaDir`, `to-other`, `<script`, `value="move.`} {
		if strings.Contains(body, bad) {
			t.Errorf("picker shows %q:\n%s", bad, body)
		}
	}
	if strings.Contains(body, `class="btn primary"`) {
		t.Error("Move here is offered where the items already are")
	}

	body = c.Post(folderURL("docs"), pick("docs/box", "a.txt")).Body.String()
	for _, want := range []string{`name="at" value="docs/box/inner"`, `name="at" value="docs"`, `name="dest" value="docs/box"`, `class="btn primary"`} {
		if !strings.Contains(body, want) {
			t.Errorf("picker at docs/box lacks %q:\n%s", want, body)
		}
	}
	if !exists(e, "docs/a.txt") {
		t.Error("walking moved something")
	}
}

func TestMoveItemsKeepsContentAndAudits(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/in/a.txt", "a")
	e.WriteFile("docs/in/f/inside.txt", "inside")
	e.Mkdir("docs/out")
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Post(folderURL("docs/in"), doMove("docs/out", "a.txt", "f"))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/files/docs/in" {
		t.Fatalf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if e.ReadFile("docs/out/a.txt") != "a" || e.ReadFile("docs/out/f/inside.txt") != "inside" {
		t.Error("items did not arrive")
	}
	if exists(e, "docs/in/a.txt") || exists(e, "docs/in/f") {
		t.Error("items are still at the source")
	}
	lines := auditedOK(t, e, "move")
	if len(lines) != 2 || lines[0]["path"] != "docs/out/a.txt" || lines[0]["from"] != "docs/in/a.txt" {
		t.Errorf("audit = %v", lines)
	}
}

func TestMoveToTheMountFolderItself(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/in/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.Post(folderURL("docs/in"), doMove("docs", "a.txt")); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if e.ReadFile("docs/a.txt") != "a" {
		t.Error("the file did not arrive")
	}
}

func TestMoveNeverReplacesAndChangesNothingWhenOneNameClashes(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/in/a.txt", "a")
	e.WriteFile("docs/in/b.txt", "new b")
	e.WriteFile("docs/out/B.TXT", "old b")
	c := e.SignIn(testenv.DefaultUser)

	rec := c.Post(folderURL("docs/in"), doMove("docs/out", "a.txt", "b.txt"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `value="move-confirm"`) {
		t.Errorf("the picker should stay open:\n%s", rec.Body)
	}
	if !exists(e, "docs/in/a.txt") || exists(e, "docs/out/a.txt") || e.ReadFile("docs/out/B.TXT") != "old b" {
		t.Error("a refused move changed something")
	}
}

func TestMoveToTheSameFolderIsRefused(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.Post(folderURL("docs"), doMove("docs", "a.txt")); rec.Code != http.StatusConflict {
		t.Errorf("status %d", rec.Code)
	}
	if !exists(e, "docs/a.txt") {
		t.Error("the file vanished")
	}
}

func TestFolderCannotMoveIntoItself(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/f/sub")
	c := e.SignIn(testenv.DefaultUser)

	for _, dest := range []string{"docs/f", "docs/f/sub"} {
		if rec := c.Post(folderURL("docs"), doMove(dest, "f")); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", dest, rec.Code)
		}
	}
	if !exists(e, "docs/f/sub") {
		t.Error("the folder changed")
	}
}

func TestMoveAcrossMountsIsRefused(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("other"))
	e.WriteFile("docs/a.txt", "a")
	e.Mkdir("docs/out")
	e.Mkdir("other/box")
	e.Symlink("docs/to-other", "../other/box")
	c := e.SignIn(testenv.DefaultUser)

	for _, dest := range []string{"other", "other/box", "docs/to-other"} {
		if rec := c.Post(folderURL("docs"), doMove(dest, "a.txt")); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", dest, rec.Code)
		}
		if rec := c.Post(folderURL("docs"), pick(dest, "a.txt")); rec.Code != http.StatusBadRequest {
			t.Errorf("picker at %s: status %d", dest, rec.Code)
		}
	}
	if !exists(e, "docs/a.txt") || exists(e, "other/a.txt") || exists(e, "other/box/a.txt") {
		t.Error("a file crossed Mounts")
	}
}

func TestMoveRefusesBadDestinations(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("docs/file.txt", "f")
	e.Mkdir("docs/.nas-files-trash/2026-01-01")
	e.Mkdir("docs/@eaDir")
	c := e.SignIn(testenv.DefaultUser)

	cases := map[string]int{
		"":                                 400,
		"/docs":                            400,
		"docs/../..":                       400,
		"docs/missing":                     404,
		"docs/file.txt":                    404,
		"docs/.nas-files-trash":            404,
		"docs/.nas-files-trash/2026-01-01": 404,
		"docs/@eaDir":                      404,
		"secret":                           404,
	}
	for dest, code := range cases {
		if rec := c.Post(folderURL("docs"), doMove(dest, "a.txt")); rec.Code != code {
			t.Errorf("%q: status %d, want %d", dest, rec.Code, code)
		}
	}
	if !exists(e, "docs/a.txt") {
		t.Error("a refused move changed something")
	}
}

func TestMovingALinkWithARelativeTargetIsRefusedAndAnAbsoluteOneMoves(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/in/target.txt", "t")
	e.Mkdir("docs/deep/er")
	e.Symlink("docs/in/relative", "target.txt")
	abs := filepath.Join(e.Dir, "docs/in/target.txt")
	if err := os.Symlink(abs, filepath.Join(e.Dir, "docs/in/absolute")); err != nil {
		t.Fatal(err)
	}
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.Post(folderURL("docs/in"), doMove("docs/deep/er", "relative")); rec.Code != http.StatusBadRequest {
		t.Errorf("relative: status %d", rec.Code)
	}
	if !exists(e, "docs/in/relative") {
		t.Error("the relative link moved")
	}
	if rec := c.Post(folderURL("docs/in"), doMove("docs/deep/er", "absolute")); rec.Code != http.StatusSeeOther {
		t.Fatalf("absolute: status %d: %s", rec.Code, rec.Body)
	}
	if got, err := os.Readlink(filepath.Join(e.Dir, "docs/deep/er/absolute")); err != nil || got != abs {
		t.Errorf("link = %q, %v", got, err)
	}
	if e.ReadFile("docs/in/target.txt") != "t" {
		t.Error("the target moved")
	}
}

func TestMoveIsRefusedWithoutWriteAccess(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.ReadOnlyMount("archive"), testenv.Mount("reading"), testenv.Mount("secret"),
		testenv.Grant(testenv.DefaultUser, "docs", "write"),
		testenv.Grant(testenv.DefaultUser, "archive", "write"),
		testenv.Grant(testenv.DefaultUser, "reading", "read"),
	)
	for _, m := range []string{"archive", "reading", "secret"} {
		e.WriteFile(m+"/a.txt", "a")
		e.Mkdir(m + "/out")
	}
	c := e.SignIn(testenv.DefaultUser)

	want := map[string]int{"archive": 403, "reading": 403, "secret": 404}
	for m, code := range want {
		for _, v := range []url.Values{pick("", "a.txt"), doMove(m+"/out", "a.txt")} {
			if rec := c.Post(folderURL(m), v); rec.Code != code {
				t.Errorf("%s %s: status %d, want %d", m, v.Get("op"), rec.Code, code)
			}
		}
		if !exists(e, m+"/a.txt") {
			t.Errorf("%s changed", m)
		}
	}
	if rec := c.Post(folderURL(""), doMove("docs", "docs")); rec.Code != http.StatusForbidden {
		t.Errorf("moving a Mount: status %d", rec.Code)
	}
}

func TestMoveNeedsACSRFTokenAndASession(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	e.Mkdir("docs/out")
	c := e.SignIn(testenv.DefaultUser)

	v := doMove("docs/out", "a.txt")
	if rec := c.PostWithoutCSRF("/files/docs", v); rec.Code != http.StatusForbidden {
		t.Errorf("without token = %d", rec.Code)
	}
	if rec := e.Anonymous().PostWithoutCSRF("/files/docs", v); rec.Code != http.StatusUnauthorized {
		t.Errorf("without session = %d", rec.Code)
	}
	if !exists(e, "docs/a.txt") {
		t.Error("a refused request moved the file")
	}
}

func TestMoveFromALinkedFolderIsJudgedByTheRealMount(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("other"))
	e.WriteFile("other/box/a.txt", "a")
	e.Mkdir("other/out")
	e.Symlink("docs/to-box", "../other/box")
	c := e.SignIn(testenv.DefaultUser)

	body := c.Post(folderURL("docs/to-box"), pick("", "a.txt")).Body.String()
	if !strings.Contains(body, `name="at" value="other"`) {
		t.Errorf("the picker should walk the real Mount:\n%s", body)
	}
	if rec := c.Post(folderURL("docs/to-box"), doMove("other/out", "a.txt")); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if e.ReadFile("other/out/a.txt") != "a" {
		t.Error("the file did not arrive")
	}
}
