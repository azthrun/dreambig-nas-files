package server_test

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The break-it test: everything a zip must never carry, reached by every route in.
func TestZipExcludesHiddenEntriesAndEverythingWithoutAccess(t *testing.T) {
	e := zipEnv(t)
	e.WriteFile("docs/proj/@eaDir/thumb.jpg", "x")
	e.WriteFile("docs/proj/sub/#recycle/old.txt", "x")
	e.WriteFile("docs/proj/.DS_Store", "x")
	e.WriteFile("docs/.nas-files-trash/gone.txt", "x")
	e.Symlink("docs/proj/to-hidden", "@eaDir")                  // Hidden Entry
	e.Symlink("docs/proj/to-trash", "../.nas-files-trash")      // the Trash
	e.Symlink("docs/proj/to-secret", "../../secret")            // Mount without Access
	e.Symlink("docs/proj/to-secret-file", "../../secret/s.txt") // file in such a Mount
	e.Symlink("docs/proj/to-outside", "/etc")                   // out of the Root
	e.Symlink("docs/proj/dangling", "nowhere")                  // broken
	e.Symlink("docs/proj/to-other", "../other.txt")             // allowed: same Mount
	c := e.SignIn("bob")
	for i, req := range []func() *httptest.ResponseRecorder{
		func() *httptest.ResponseRecorder { return c.Get("/files/docs/proj?zip=1") },
		func() *httptest.ResponseRecorder {
			return c.Post("/files/docs", url.Values{"op": {"zip"}, "names": {"proj", "@eaDir", ".nas-files-trash", "nope"}})
		},
	} {
		got, _ := readZip(t, req())
		for n, v := range got {
			for _, bad := range []string{"eaDir", "recycle", "DS_Store", "trash", "thumb", "gone", "secret", "s.txt", "outside", "dangling", "hidden"} {
				if strings.Contains(n, bad) {
					t.Errorf("route %d: zip leaked %q", i, n)
				}
			}
			if v == "top" || v == "x" {
				t.Errorf("route %d: forbidden content in %q", i, n)
			}
		}
		if _, ok := got[strings.TrimSuffix(firstPrefix(got), "/")+"/to-other"]; !ok {
			t.Errorf("route %d: same-Mount link missing: %v", i, names(got))
		}
	}
}

func firstPrefix(m map[string]string) string {
	for n := range m {
		if strings.HasPrefix(n, "docs/proj") {
			return "docs/proj"
		}
		if strings.HasPrefix(n, "proj") {
			return "proj"
		}
	}
	return ""
}

func TestZipRefusalsLookLikeNotFound(t *testing.T) {
	e := zipEnv(t)
	c := e.SignIn("bob")
	if rec := c.Get("/files/secret?zip=1"); rec.Code != 404 {
		t.Errorf("folder without Access = %d", rec.Code)
	}
	if rec := c.Post("/files/secret", url.Values{"op": {"zip"}, "names": {"s.txt"}}); rec.Code != 404 {
		t.Errorf("selection without Access = %d", rec.Code)
	}
	if rec := c.Post("/files/docs", url.Values{"op": {"zip"}, "names": {"../secret/s.txt", "a/b"}}); rec.Code != 404 {
		t.Errorf("traversal names = %d", rec.Code)
	}
	if rec := c.Get("/files/docs/proj/a.txt?zip=1"); rec.Code != 200 || rec.Body.String() != "aaa" {
		t.Errorf("?zip on a file should just download it: %d", rec.Code)
	}
}

func TestZipSurvivesSymlinkLoops(t *testing.T) {
	e := zipEnv(t)
	e.Symlink("docs/proj/sub/up", "..") // back to an ancestor
	e.Symlink("docs/proj/self", "self") // self loop
	e.Symlink("docs/proj/ping", "pong") // two-link loop
	e.Symlink("docs/proj/pong", "ping")
	got, _ := readZip(t, e.SignIn("alice").Get("/files/docs?zip=1"))
	if got["docs/proj/sub/b.txt"] != "bbbb" {
		t.Errorf("lost real content: %v", names(got))
	}
	if len(got) > 40 {
		t.Errorf("walk did not stop at loops: %d entries", len(got))
	}
}
