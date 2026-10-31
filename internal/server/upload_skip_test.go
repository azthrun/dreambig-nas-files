package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// skip reports the User's choice to skip a file, as the upload panel does when the
// Conflict dialog is answered before any data is sent.
func skip(c *testenv.Client, path string) *httptest.ResponseRecorder {
	r := tusReq(c, http.MethodPost, "/uploads/skip", url.Values{"path": {path}}.Encode(), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	return c.Do(r)
}

func TestSkippingAtCreationIsReportedAndAudited(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/a.txt", "old")
	c := e.SignIn("alice")

	if rec := skip(c, "docs/a.txt"); rec.Code != http.StatusNoContent {
		t.Fatalf("skip = %d: %s", rec.Code, rec.Body)
	}
	l := lastAuditOf(t, e, "upload_skip")
	if l == nil || l["user"] != "alice" || l["path"] != "docs/a.txt" || l["choice"] != "skip" || l["outcome"] != "ok" {
		t.Fatalf("skip not audited with the choice: %v", l)
	}
	if got := e.ReadFile("docs/a.txt"); got != "old" {
		t.Errorf("file = %q, skip must change nothing", got)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("skip created an Upload")
	}
}

func TestSkipIsHeldToTheSameRulesAsAnUpload(t *testing.T) {
	e := upEnv(t)
	e.Mkdir("docs/@eaDir")
	alice, bob := e.SignIn("alice"), e.SignIn("bob")

	for _, tc := range []struct {
		name string
		c    *testenv.Client
		path string
		want int
	}{
		{"invalid Path", alice, "docs/../x", http.StatusBadRequest},
		{"a bad name", alice, "docs/a:b", http.StatusBadRequest},
		{"a Hidden Entry", alice, "docs/@eaDir/x", http.StatusNotFound},
		{"no such Mount", alice, "nowhere/x", http.StatusNotFound},
		{"read Access", bob, "archive/x", http.StatusForbidden},
		{"a read-only Mount", alice, "archive/x", http.StatusForbidden},
		{"the Root", alice, "", http.StatusBadRequest},
	} {
		if rec := skip(tc.c, tc.path); rec.Code != tc.want {
			t.Errorf("%s: skip = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	if l := lastAuditOf(t, e, "upload_skip"); l == nil || l["outcome"] != "refused" {
		t.Errorf("refused skips are not audited: %v", l)
	}
	for _, l := range auditLines(t, e) {
		if l["operation"] == "upload_skip" && l["outcome"] == "ok" {
			t.Errorf("a refused skip was audited as ok: %v", l)
		}
	}
}

func TestSkipNeedsASessionAndACSRFToken(t *testing.T) {
	e := upEnv(t)
	form := url.Values{"path": {"docs/a.txt"}}.Encode()
	post := func(c *testenv.Client, csrf bool) int {
		r := httptest.NewRequest(http.MethodPost, "/uploads/skip", strings.NewReader(form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if csrf {
			r.Header.Set("X-CSRF-Token", c.CSRF)
		}
		return c.Do(r).Code
	}
	if code := post(e.Anonymous(), true); code != http.StatusUnauthorized {
		t.Errorf("without a session = %d", code)
	}
	alice := e.SignIn("alice")
	if code := post(alice, false); code != http.StatusForbidden {
		t.Errorf("without a CSRF token = %d", code)
	}
	if rec := alice.Do(tusReq(alice, http.MethodGet, "/uploads/skip", "", nil)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET skip = %d, want 405", rec.Code)
	}
}
