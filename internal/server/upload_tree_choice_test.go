package server_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// A Folder Upload names a Path whose parent folders may not exist yet; the Conflict
// choice must still travel with it, and the folders are made when the file completes.
func TestTreeUploadWithMissingParentsAndAChoiceCreatesThemAndPlaces(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	for _, choice := range []string{"keep", "replace"} {
		path := "docs/tree-" + choice + "/a/b/x.txt"
		if rec := uploadWith(t, c, path, choice, "data"); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: PATCH = %d: %s", choice, rec.Code, rec.Body)
		}
		if got := e.ReadFile(path); got != "data" {
			t.Errorf("%s: file = %q", choice, got)
		}
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}
}

func TestTreeUploadWithAChoiceStillRefusesABadNewParentName(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	if rec := tusCreateChoice(c, "docs/new/@eaDir/x.txt", "keep", 1); rec.Code < 400 {
		t.Errorf("create under a Hidden Entry name = %d, want a refusal", rec.Code)
	}
}

// A late Conflict in a Folder Upload is settled by the same resolve endpoint, and
// resolving recreates the parent chain if it is gone by then.
func TestResolveWorksWhenTheParentChainIsNew(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	for _, choice := range []string{"keep", "replace"} {
		dir := "docs/resolved-" + choice
		path := dir + "/a/x.txt"
		loc := uploadURL(t, tusCreate(c, path, 6)) // the folders do not exist yet
		tusPatch(c, loc, 0, "new")
		e.WriteFile(path, "someone else")
		if rec := tusPatch(c, loc, 3, "new"); rec.Code != http.StatusConflict {
			t.Fatalf("%s: last PATCH = %d, want 409", choice, rec.Code)
		}
		if err := os.RemoveAll(filepath.Join(e.Dir, filepath.FromSlash(dir))); err != nil {
			t.Fatal(err)
		}
		if rec := resolve(c, loc, choice); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: resolve = %d: %s", choice, rec.Code, rec.Body)
		}
		if got := e.ReadFile(path); got != "newnew" {
			t.Errorf("%s: file = %q", choice, got)
		}
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}
}

func TestResolveKeepAndReplaceAlsoWorkWhenTheFileIsStillThereUnderNewlyMadeParents(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	path := "docs/still/a/x.txt"
	loc := uploadURL(t, tusCreate(c, path, 3))
	e.WriteFile(path, "someone else")
	if rec := tusPatch(c, loc, 0, "new"); rec.Code != http.StatusConflict {
		t.Fatalf("PATCH = %d, want 409", rec.Code)
	}
	if rec := resolve(c, loc, "keep"); rec.Code != http.StatusNoContent {
		t.Fatalf("resolve keep = %d: %s", rec.Code, rec.Body)
	}
	if e.ReadFile(path) != "someone else" || e.ReadFile("docs/still/a/x (1).txt") != "new" {
		t.Error("keep both did not leave both files")
	}
}
