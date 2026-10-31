package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
	rec := bob.Post("/files/secret", mkdirForm("x"))
	if rec.Code != http.StatusNotFound || rec.Body.String() != bob.Post("/files/nonexistent", mkdirForm("x")).Body.String() {
		t.Fatalf("no-Access mkdir = %d, must look like a missing Path", rec.Code)
	}
	if ls := refusalsOf(t, e, "mkdir"); len(ls) == 0 || ls[0]["path"] != "secret" || ls[0]["user"] != "bob" {
		t.Fatalf("want a refused mkdir line for the no-Access Mount, got %v", ls)
	}
}

// A write is never routine navigation: unlike a browse, it is audited even when all that
// is wrong is that the parent is missing.
func TestMkdirInAMissingFolderIsAudited(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	if rec := c.Post("/files/docs/nope", mkdirForm("x")); rec.Code != http.StatusNotFound {
		t.Fatalf("missing parent = %d", rec.Code)
	}
	if ls := refusalsOf(t, e, "mkdir"); len(ls) != 1 || ls[0]["path"] != "docs/nope" {
		t.Fatalf("want one refused mkdir line, got %v", ls)
	}
}

func TestAZipAuditsWhatWasSelectedAndEveryDroppedName(t *testing.T) {
	e := zipEnv(t)
	e.WriteFile("docs/proj/@eaDir/x", "x")
	e.Symlink("docs/proj/link", "../../secret/s.txt")
	bob := e.SignIn("bob")
	rec := bob.Post("/files/docs/proj", url.Values{"op": {"zip"}, "names": {"a.txt", "sub", "missing.txt", "@eaDir", "link", "../other.txt"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("zip = %d", rec.Code)
	}
	ls := refusalsOf(t, e, "zip")
	var got []string
	for _, l := range ls {
		p, _ := l["path"].(string)
		got = append(got, p)
		if l["user"] != "bob" {
			t.Errorf("line without the User: %v", l)
		}
	}
	want := []string{"docs/proj/missing.txt", "docs/proj/@eaDir", "docs/proj/link", "docs/proj"}
	if !slices.Equal(got, want) {
		t.Errorf("dropped names audited as %q, want %q", got, want)
	}
	var ok map[string]any
	for _, a := range auditLines(t, e) {
		if a["operation"] == "zip" && a["outcome"] == "ok" {
			ok = a
		}
	}
	items, _ := ok["items"].([]any)
	if ok["path"] != "docs/proj" || len(items) != 2 || items[0] != "docs/proj/a.txt" || items[1] != "docs/proj/sub" {
		t.Errorf("zip line does not name the selection: %v", ok)
	}
}

func TestAZipOfNothingButBadNamesAuditsEachNameOnce(t *testing.T) {
	e := zipEnv(t)
	c := e.SignIn("alice")
	if rec := c.Post("/files/docs/proj", url.Values{"op": {"zip"}, "names": {"gone.txt"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("zip = %d", rec.Code)
	}
	if ls := refusalsOf(t, e, "zip"); len(ls) != 1 || ls[0]["path"] != "docs/proj/gone.txt" || ls[0]["status"] != float64(404) {
		t.Fatalf("want one refused zip line for the name, got %v", ls)
	}
}

func TestAZipOfAMissingFolderIsAudited(t *testing.T) {
	e := zipEnv(t)
	c := e.SignIn("alice")
	if rec := c.Post("/files/docs/nope", url.Values{"op": {"zip"}, "names": {"a"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("zip = %d", rec.Code)
	}
	if ls := refusalsOf(t, e, "zip"); len(ls) != 1 || ls[0]["path"] != "docs/nope" {
		t.Fatalf("want one refused zip line, got %v", ls)
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

// An Upload request refused for its session or CSRF token names the Path where the
// request carries one (tus metadata on creation, the form field on a skip report), and
// the response never echoes it.
func TestUploadSessionAndCSRFRefusalsNameThePathWithoutEchoingIt(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	create := func(c *testenv.Client, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/uploads/", nil)
		r.Header.Set("Tus-Resumable", "1.0.0")
		r.Header.Set("Upload-Length", "3")
		r.Header.Set("Upload-Metadata", tusMeta(map[string]string{"path": "docs/new.txt"}))
		if csrf {
			r.Header.Set("X-CSRF-Token", c.CSRF)
		}
		return c.Do(r)
	}
	skipForm := func(c *testenv.Client, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/uploads/skip", strings.NewReader(url.Values{"path": {"docs/skipped.txt"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if csrf {
			r.Header.Set("X-CSRF-Token", c.CSRF)
		}
		return c.Do(r)
	}
	signedIn := e.SignIn(testenv.DefaultUser)
	var recs []*httptest.ResponseRecorder
	recs = append(recs, create(e.Anonymous(), false), skipForm(e.Anonymous(), false),
		create(signedIn, false), skipForm(signedIn, false))
	// An id-only request names no Path: nothing to report.
	idOnly := httptest.NewRequest(http.MethodPatch, "/uploads/0123456789abcdef0123456789abcdef", nil)
	e.Anonymous().Do(idOnly)

	for _, rec := range recs {
		if b := rec.Body.String(); strings.Contains(b, "new.txt") || strings.Contains(b, "skipped.txt") {
			t.Errorf("a refusal echoes the Path: %q", b)
		}
	}
	paths := func(op string) (out []string) {
		for _, l := range refusalsOf(t, e, op) {
			p, _ := l["path"].(string)
			out = append(out, p)
		}
		return out
	}
	if got := strings.Join(paths("session"), ","); got != "docs/new.txt,docs/skipped.txt," {
		t.Errorf("session refusals name %q", got)
	}
	if got := strings.Join(paths("csrf"), ","); got != "docs/new.txt,docs/skipped.txt" {
		t.Errorf("CSRF refusals name %q", got)
	}
}
