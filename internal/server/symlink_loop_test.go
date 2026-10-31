package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func TestASymlinkLoopLooksLikeAMissingPath(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Symlink("docs/self", "self")
	e.Symlink("docs/a", "b")
	e.Symlink("docs/b", "a")
	c := e.SignIn(testenv.DefaultUser)

	want := c.Get("/files/docs/nonexistent")
	for _, target := range []string{"/files/docs/self", "/files/docs/a", "/files/docs/a/x"} {
		got := c.Get(target)
		if got.Code != http.StatusNotFound || got.Body.String() != want.Body.String() {
			t.Errorf("GET %s = %d, want the same 404 as a missing Path", target, got.Code)
		}
	}
	// The loops are not listed either.
	if body := c.Get("/files/docs").Body.String(); body == "" || strings.Contains(body, ">self<") || strings.Contains(body, ">a<") {
		t.Errorf("a looping link is listed:\n%s", body)
	}
}
