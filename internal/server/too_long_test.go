package server_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

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

// Every name is fine here but the whole Path is longer than the system takes: os.Root
// could still create it, one folder at a time, and nothing could ever reach it again.
func TestAPathTooLongForTheSystemIsNotCreated(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	var segs []string
	for len(strings.Join(segs, "/")) < 5000 {
		segs = append(segs, strings.Repeat("a", 200))
	}
	deep := "docs/" + strings.Join(segs, "/")

	if rec := uploadWith(t, c, deep+"/f.txt", "", "abc"); rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH = %d %q, want a bad name", rec.Code, rec.Body)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}

	// The refused Upload left the folders, so mkdir can walk down to the limit. Whatever
	// it creates must stay reachable.
	refused := false
	for i := range segs {
		parent := "/files/docs/" + strings.Join(segs[:i], "/")
		name := strings.Repeat("b", 200)
		switch rec := c.Post(strings.TrimSuffix(parent, "/"), mkdirForm(name)); rec.Code {
		case http.StatusSeeOther:
			if got := c.Get(strings.TrimSuffix(parent, "/") + "/" + url.PathEscape(name)); got.Code != http.StatusOK {
				t.Fatalf("depth %d: the new folder answers %d", i, got.Code)
			}
		case http.StatusBadRequest:
			refused = true
		case http.StatusNotFound: // the parent is out of reach already
		default:
			t.Fatalf("depth %d: mkdir = %d", i, rec.Code)
		}
	}
	if !refused {
		t.Error("no mkdir reached the limit")
	}
}
