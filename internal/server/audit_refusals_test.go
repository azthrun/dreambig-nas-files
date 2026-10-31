package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// refusalsOf returns the audit lines of a refused operation, in order.
func refusalsOf(t *testing.T, e *testenv.Env, op string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range auditLines(t, e) {
		if l["operation"] == op && l["outcome"] == "refused" {
			out = append(out, l)
		}
	}
	return out
}

func TestAMountWithoutAccessIsAuditedButAnswersLikeAMissingPath(t *testing.T) {
	e := accessEnv(t)
	bob := e.SignIn("bob")

	got, want := bob.Get("/files/secret/s.txt"), bob.Get("/files/docs/nonexistent.txt")
	if got.Code != http.StatusNotFound || got.Body.String() != want.Body.String() {
		t.Fatalf("no-Access answer differs from a missing Path: %d vs %d", got.Code, want.Code)
	}
	ls := refusalsOf(t, e, "browse")
	if len(ls) != 1 || ls[0]["path"] != "secret/s.txt" || ls[0]["user"] != "bob" || ls[0]["status"] != float64(404) {
		t.Fatalf("want one refused browse line for secret/s.txt (the plain miss stays unaudited), got %v", ls)
	}
}

func TestMkdirInAMountWithoutAccessIsAudited(t *testing.T) {
	e := accessEnv(t)
	bob := e.SignIn("bob")
	missing := bob.Post("/files/docs/nope", mkdirForm("x"))
	rec := bob.Post("/files/secret", mkdirForm("x"))
	if rec.Code != http.StatusNotFound || rec.Body.String() != bob.Post("/files/nonexistent", mkdirForm("x")).Body.String() {
		t.Fatalf("no-Access mkdir = %d, must look like a missing Path", rec.Code)
	}
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing parent = %d", missing.Code)
	}
	ls := refusalsOf(t, e, "mkdir")
	if len(ls) != 1 || ls[0]["path"] != "secret" || ls[0]["user"] != "bob" {
		t.Fatalf("want one refused mkdir line for the no-Access Mount, got %v", ls)
	}
}

func TestAGetWithoutASessionIsAuditedWithItsPath(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	rec := e.Anonymous().Get("/files/docs/a.txt")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("anonymous GET = %d, want the redirect to sign-in", rec.Code)
	}
	ls := refusalsOf(t, e, "session")
	if len(ls) != 1 || ls[0]["path"] != "docs/a.txt" {
		t.Fatalf("want one refused session line naming docs/a.txt, got %v", ls)
	}
}

func TestSessionAndCSRFRefusalsNameThePath(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/tax")

	e.Anonymous().PostWithoutCSRF("/files/docs/tax", mkdirForm("x"))
	c := e.SignIn(testenv.DefaultUser)
	c.PostWithoutCSRF("/files/docs/tax", mkdirForm("x"))

	if ls := refusalsOf(t, e, "session"); len(ls) != 1 || ls[0]["path"] != "docs/tax" {
		t.Errorf("session refusal lacks the Path: %v", ls)
	}
	if ls := refusalsOf(t, e, "csrf"); len(ls) != 1 || ls[0]["path"] != "docs/tax" || ls[0]["user"] != testenv.DefaultUser {
		t.Errorf("CSRF refusal lacks the Path: %v", ls)
	}
}

func TestZipWithNothingSelectedIsAudited(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/tax")
	c := e.SignIn(testenv.DefaultUser)
	if rec := c.Post("/files/docs/tax", url.Values{"op": {"zip"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty zip = %d", rec.Code)
	}
	if ls := refusalsOf(t, e, "zip"); len(ls) != 1 || ls[0]["path"] != "docs/tax" || ls[0]["status"] != float64(400) {
		t.Fatalf("want one refused zip line, got %v", ls)
	}
}

func TestAnUnknownFormOperationAndAWrongMethodAreAudited(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/tax")
	c := e.SignIn(testenv.DefaultUser)
	if rec := c.Post("/files/docs/tax", url.Values{"op": {"frobnicate"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown op = %d", rec.Code)
	}
	r := httptest.NewRequest(http.MethodPut, "/files/docs/tax", strings.NewReader(""))
	r.Header.Set("X-CSRF-Token", c.CSRF)
	if rec := c.Do(r); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d", rec.Code)
	}
	ls := refusalsOf(t, e, "files")
	if len(ls) != 2 || ls[0]["status"] != float64(400) || ls[1]["status"] != float64(405) || ls[0]["path"] != "docs/tax" {
		t.Fatalf("want a 400 and a 405 line for /files, got %v", ls)
	}
}
