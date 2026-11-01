package server_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// A name longer than the filesystem takes is just another missing Path. Answering it
// differently for a real Mount would tell any User which Mounts exist.
func TestAnOverlongNameCannotProbeForAMount(t *testing.T) {
	e := accessEnv(t)
	bob := e.SignIn("bob")
	long := strings.Repeat("a", 300)

	got, want := bob.Get("/files/secret/"+long), bob.Get("/files/nonexistent/"+long)
	if got.Code != http.StatusNotFound || got.Body.String() != want.Body.String() {
		t.Errorf("browse = %d, want the same 404 as no such Mount (%d)", got.Code, want.Code)
	}
	got, want = bob.Post("/files/secret/"+long, mkdirForm("x")), bob.Post("/files/nonexistent/"+long, mkdirForm("x"))
	if got.Code != http.StatusNotFound || got.Body.String() != want.Body.String() {
		t.Errorf("mkdir = %d, want the same 404 as no such Mount (%d)", got.Code, want.Code)
	}
	got, want = tusCreate(bob, "secret/"+long+"/f.txt", 3), tusCreate(bob, "nonexistent/"+long+"/f.txt", 3)
	if got.Code != http.StatusNotFound || got.Body.String() != want.Body.String() {
		t.Errorf("Upload = %d %q, want the same 404 as no such Mount (%d %q)", got.Code, got.Body, want.Code, want.Body)
	}
	for _, l := range auditLines(t, e) {
		if l["outcome"] == "failed" {
			t.Errorf("a refusal was audited as a failure: %v", l)
		}
	}
}

func TestAnOverlongNameInAVisibleMountIsAMiss(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	got, want := c.Get("/files/docs/"+strings.Repeat("a", 300)), c.Get("/files/docs/nonexistent")
	if got.Code != http.StatusNotFound || got.Body.String() != want.Body.String() {
		t.Fatalf("browse = %d, want the same 404 as a missing Path", got.Code)
	}
	if ls := auditLines(t, e); len(ls) != 1 { // the sign-in
		t.Errorf("a plain miss was audited: %v", ls)
	}
}

func TestAFolderUploadWithAnOverlongParentIsABadName(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	rec := tusCreate(c, "docs/"+strings.Repeat("a", 256)+"/f.txt", 3)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ERR_INVALID_PATH") {
		t.Fatalf("create = %d %q, want a bad name", rec.Code, rec.Body)
	}
}

// Every name is fine here but the whole Path is longer than the system takes, and none
// of its folders exist yet. os.Root could still create them, one at a time, and nothing
// could ever reach them again. The refusal comes before any data, and leaves nothing.
func TestAPathTooLongForTheSystemIsNotCreated(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	segs := []string{}
	for len(strings.Join(segs, "/")) < 5000 {
		segs = append(segs, strings.Repeat("a", 200))
	}
	rec := tusCreate(c, "docs/"+strings.Join(segs, "/")+"/f.txt", 3)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ERR_INVALID_PATH") {
		t.Fatalf("create = %d %q, want a bad name before any data", rec.Code, rec.Body)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}
	if exists(e, "docs/"+segs[0]) {
		t.Error("a folder was created")
	}
	if ls := refusalsOf(t, e, "upload_create"); len(ls) != 1 {
		t.Errorf("want the refusal audited, got %v", ls)
	}
}

// deepFolder makes folders with mkdir until a name of room bytes, 11 to 210, ends the
// longest absolute path the system takes. It returns the folder's Path, with room.
func deepFolder(t *testing.T, e *testenv.Env, c *testenv.Client) (string, int) {
	t.Helper()
	base, err := filepath.EvalSymlinks(filepath.Join(e.Dir, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	dir := "docs"
	used := len(base)
	for {
		room := unix.PathMax - 1 - used - 1 // the separator before the name
		if room <= 210 {
			return dir, room
		}
		name := strings.Repeat("a", 200)
		if rec := c.Post("/files/"+dir, mkdirForm(name)); rec.Code != http.StatusSeeOther {
			t.Fatalf("mkdir at %d bytes = %d", used, rec.Code)
		}
		dir, used = dir+"/"+name, used+201
	}
}

// The limit is exact: what mkdir creates can always be reached, and one byte more is
// refused with nothing created.
func TestMkdirStopsAtTheSystemPathLimit(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	dir, room := deepFolder(t, e, c)

	fits := strings.Repeat("b", room)
	if rec := c.Post("/files/"+dir, mkdirForm(fits)); rec.Code != http.StatusSeeOther {
		t.Fatalf("mkdir at the limit = %d", rec.Code)
	}
	if got := c.Get("/files/" + dir + "/" + fits); got.Code != http.StatusOK {
		t.Errorf("the new folder answers %d", got.Code)
	}
	over := strings.Repeat("c", room+1)
	rec := c.Post("/files/"+dir, mkdirForm(over))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("mkdir past the limit = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), over) {
		t.Error("the refusal lost the typed name")
	}
	if exists(e, dir+"/"+over) {
		t.Error("a folder past the limit was created")
	}
	if got := c.Get("/files/" + dir); !strings.Contains(got.Body.String(), fits) || strings.Contains(got.Body.String(), over) {
		t.Error("the listing does not show just the folder that fits")
	}
}

// The number keep both adds can carry a Path that fits over the limit.
func TestKeepBothCannotPlaceAFilePastTheSystemPathLimit(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	dir, room := deepFolder(t, e, c)
	path := dir + "/" + strings.Repeat("f", room)

	if rec := uploadWith(t, c, path, "", "abc"); rec.Code != http.StatusNoContent {
		t.Fatalf("upload at the limit = %d %q", rec.Code, rec.Body)
	}
	if got := c.Get("/files/" + path); got.Code != http.StatusOK {
		t.Fatalf("the file answers %d", got.Code)
	}
	if rec := uploadWith(t, c, path, "keep", "xyz"); rec.Code != http.StatusBadRequest {
		t.Errorf("keep both = %d %q, want a bad name", rec.Code, rec.Body)
	}
	if exists(e, path+" (1)") {
		t.Error("a file past the limit was placed")
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}
}

// The same for the limit on one name: the number carries it past NAME_MAX.
func TestKeepBothCannotPlaceAFilePastTheSystemNameLimit(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	stem := strings.Repeat("f", 251)
	path := "docs/" + stem + ".txt"

	if rec := uploadWith(t, c, path, "", "abc"); rec.Code != http.StatusNoContent {
		t.Fatalf("upload at the limit = %d %q", rec.Code, rec.Body)
	}
	rec := uploadWith(t, c, path, "keep", "xyz")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ERR_INVALID_PATH") {
		t.Errorf("keep both = %d %q, want a bad name", rec.Code, rec.Body)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}
	if ls := refusalsOf(t, e, "upload"); len(ls) != 1 {
		t.Errorf("want the refusal audited, got %v", ls)
	}
}
