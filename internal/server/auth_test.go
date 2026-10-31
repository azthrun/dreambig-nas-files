package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func auditLines(t *testing.T, e *testenv.Env) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(e.Audit.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("audit line is not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func TestSignedInRootListsMounts(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.ReadOnlyMount("archive"))
	c := e.SignIn(testenv.DefaultUser)
	rec := c.Get("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"docs", "archive", testenv.DefaultUser} {
		if !strings.Contains(body, want) {
			t.Errorf("Root page lacks %q", want)
		}
	}
	if strings.Contains(body, e.Dir) {
		t.Error("Root page leaks the absolute NAS path")
	}
}

func TestEverythingButSignInAndHealthNeedsASession(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	anon := e.Anonymous()

	rec := anon.Get("/")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET / without session = %d %q, want redirect to /login", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(rec.Body.String(), "docs") {
		t.Error("unauthenticated response lists Mounts")
	}
	if rec := anon.PostWithoutCSRF("/logout", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /logout without session = %d, want 401", rec.Code)
	}
	if rec := anon.Get("/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d", rec.Code)
	}
	if rec := anon.Get("/login"); rec.Code != http.StatusOK {
		t.Errorf("/login = %d", rec.Code)
	}
}

func TestSignInSetsASafeSessionCookie(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	rec := e.Anonymous().PostWithoutCSRF("/login", url.Values{"name": {testenv.DefaultUser}, "password": {e.Password(testenv.DefaultUser)}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("sign-in = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("got %d cookies", len(cs))
	}
	c := cs[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure {
		t.Errorf("cookie flags: HttpOnly=%v SameSite=%v Secure=%v", c.HttpOnly, c.SameSite, c.Secure)
	}
	if c.MaxAge != 30*24*60*60 {
		t.Errorf("MaxAge = %d, want 30 days", c.MaxAge)
	}
	if c.Value == "" || c.Path != "/" {
		t.Errorf("cookie value/path: %q %q", c.Value, c.Path)
	}
}

func TestFailuresShowOneGenericMessage(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	bad := func(name, pw string) string {
		rec := e.Anonymous().PostWithoutCSRF("/login", url.Values{"name": {name}, "password": {pw}})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status %d", rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Error("failed sign-in set a cookie")
		}
		return rec.Body.String()
	}
	wrongPassword := bad(testenv.DefaultUser, "wrong")
	unknownUser := bad("nobody", "wrong")
	if !strings.Contains(wrongPassword, "Sign-in failed") {
		t.Errorf("no failure message: %q", wrongPassword)
	}
	if wrongPassword != unknownUser {
		// Bodies may differ only by the echoed name field; strip it.
		strip := func(s, n string) string { return strings.ReplaceAll(s, n, "") }
		if strip(wrongPassword, testenv.DefaultUser) != strip(unknownUser, "nobody") {
			t.Error("failure message differs between wrong password and unknown name")
		}
	}
}

func TestSessionExpiresAfter30DaysWithoutUseAndSlidesWhenUsed(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)

	for i := 0; i < 3; i++ { // 60 days in total, never idle for 30
		e.Clock.Advance(20 * 24 * time.Hour)
		rec := c.Get("/")
		if rec.Code != http.StatusOK {
			t.Fatalf("active session expired after %d x 20 days: %d", i+1, rec.Code)
		}
		cs := rec.Result().Cookies()
		if len(cs) != 1 || cs[0].MaxAge != 30*24*60*60 {
			t.Fatalf("cookie not renewed: %v", cs)
		}
	}
	e.Clock.Advance(31 * 24 * time.Hour)
	if rec := c.Get("/"); rec.Code != http.StatusSeeOther {
		t.Errorf("idle session still valid after 31 days: %d", rec.Code)
	}
}

func TestSignOutEndsTheSession(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	rec := c.Post("/logout", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("sign-out = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := c.Get("/"); rec.Code != http.StatusSeeOther {
		t.Errorf("session survives sign-out: %d", rec.Code)
	}
}

func TestWritesNeedACSRFToken(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)

	if rec := c.PostWithoutCSRF("/logout", nil); rec.Code != http.StatusForbidden {
		t.Errorf("POST without token = %d, want 403", rec.Code)
	}
	if rec := c.PostWithToken("/logout", nil, strings.Repeat("x", 43)); rec.Code != http.StatusForbidden {
		t.Errorf("POST with wrong token = %d, want 403", rec.Code)
	}
	if rec := c.Get("/"); rec.Code != http.StatusOK {
		t.Errorf("a refused write ended the session: %d", rec.Code)
	}
	// The token also works from a header, which scripted (tus) requests will use.
	if rec := c.PostWithHeaderToken("/logout", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("POST with header token = %d, want 303", rec.Code)
	}
}

func TestOneUsersTokenDoesNotWorkForAnother(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.WithUser("bob"))
	a := e.SignIn(testenv.DefaultUser)
	b := e.SignIn("bob")
	if rec := b.PostWithToken("/logout", nil, a.CSRF); rec.Code != http.StatusForbidden {
		t.Errorf("another session's token accepted: %d", rec.Code)
	}
}

func TestRepeatedFailuresAreRateLimitedByUserAndByIP(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.WithUser("bob"))
	fail := func(name, addr string) int {
		return e.Anonymous().From(addr).PostWithoutCSRF("/login", url.Values{"name": {name}, "password": {"wrong"}}).Code
	}
	// By User: failures from many addresses lock the name, even for the right password.
	for i := 0; i < 5; i++ {
		if got := fail(testenv.DefaultUser, "192.0.2."+string(rune('1'+i))+":1000"); got != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d", i+1, got)
		}
	}
	rec := e.Anonymous().From("192.0.2.99:1000").PostWithoutCSRF("/login", url.Values{"name": {testenv.DefaultUser}, "password": {e.Password(testenv.DefaultUser)}})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("locked User signed in or got %d", rec.Code)
	}
	// Other Users from other addresses are unaffected.
	if rec := e.Anonymous().From("192.0.2.98:1000").PostWithoutCSRF("/login", url.Values{"name": {"bob"}, "password": {e.Password("bob")}}); rec.Code != http.StatusSeeOther {
		t.Errorf("unrelated sign-in = %d", rec.Code)
	}
	// By IP: one address guessing many names gets locked.
	for i := 0; i < 5; i++ {
		fail("guess"+string(rune('a'+i)), "198.51.100.7:1000")
	}
	if got := fail("bob", "198.51.100.7:2000"); got != http.StatusTooManyRequests {
		t.Errorf("locked address got %d", got)
	}
	// The window passes.
	e.Clock.Advance(16 * time.Minute)
	if rec := e.Anonymous().From("192.0.2.99:1000").PostWithoutCSRF("/login", url.Values{"name": {testenv.DefaultUser}, "password": {e.Password(testenv.DefaultUser)}}); rec.Code != http.StatusSeeOther {
		t.Errorf("sign-in after the window = %d", rec.Code)
	}
}

func TestAuditNamesTheUserForSignInsAndRefusals(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Anonymous().PostWithoutCSRF("/login", url.Values{"name": {"mallory"}, "password": {"wrong"}})
	c := e.SignIn(testenv.DefaultUser)
	c.PostWithoutCSRF("/logout", nil)
	c.Post("/logout", nil)
	e.Anonymous().Get("/")

	type want struct{ user, op, outcome string }
	var got []want
	for _, l := range auditLines(t, e) {
		user, _ := l["user"].(string) // absent on a refusal before any sign-in
		got = append(got, want{user, l["operation"].(string), l["outcome"].(string)})
		if l["time"] == nil {
			t.Errorf("line without time: %v", l)
		}
	}
	exp := []want{
		{"mallory", "signin", "refused"},
		{testenv.DefaultUser, "signin", "ok"},
		{testenv.DefaultUser, "csrf", "refused"},
		{testenv.DefaultUser, "signout", "ok"},
		{"", "session", "refused"}, // the anonymous GET of the Root
	}
	if len(got) < len(exp) {
		t.Fatalf("audit lines: %v", got)
	}
	for i, w := range exp {
		if got[i] != w {
			t.Errorf("line %d = %v, want %v", i, got[i], w)
		}
	}
	if strings.Contains(e.Audit.String(), "wrong") {
		t.Error("audit log contains a password")
	}
}
