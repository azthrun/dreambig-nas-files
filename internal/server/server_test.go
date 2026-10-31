package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func TestHealthzAnswersWithoutASession(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	rec := e.Get("/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("the health check must not start a session")
	}
}

func TestHealthzRevealsNothingAboutMounts(t *testing.T) {
	e := testenv.New(t, testenv.Mount("secret-mount"))
	body := e.Get("/healthz").Body.String()
	if strings.Contains(body, "secret-mount") || strings.Contains(body, e.Dir) {
		t.Errorf("health body leaks configuration: %q", body)
	}
}

func TestHealthzOnlyAnswersGet(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	rec := httptest.NewRecorder()
	e.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", rec.Code)
	}
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	if rec := e.Get("/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

func TestAnUnknownRouteAnswersInTheUsersLanguageAndIsAudited(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	c.AcceptLanguage = "zh-Hans"
	rec := c.Get("/nope")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), i18n.T("zh-Hans", "error.not_found")) {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "nope") {
		t.Error("the page echoes the refused address")
	}
	anon := e.Anonymous()
	anon.AcceptLanguage = "zh-Hans"
	rec = anon.PostWithoutCSRF("/healthz", nil)
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), i18n.T("zh-Hans", "error.method")) {
		t.Fatalf("POST /healthz = %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Allow"); !strings.Contains(got, "GET") {
		t.Errorf("Allow = %q", got)
	}
	ls := refusalsOf(t, e, "route")
	if len(ls) != 2 || ls[0]["user"] != testenv.DefaultUser || ls[0]["status"] != float64(404) || ls[1]["status"] != float64(405) {
		t.Fatalf("want a 404 line for alice and a 405 line, got %v", ls)
	}
}

func TestStartupRefusesNoMounts(t *testing.T) {
	if _, err := testenv.TryNew(t); err == nil {
		t.Fatal("an app with no Mounts must not start")
	}
}

func TestHarnessMarksReadOnlyMounts(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.ReadOnlyMount("archive"))
	for _, m := range e.Mounts {
		if got, want := m.ReadOnly, m.Name == "archive"; got != want {
			t.Errorf("%s ReadOnly = %v, want %v", m.Name, got, want)
		}
	}
}

func TestFakeClockDrivesTheApp(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	before := e.Clock.Now()
	e.Clock.Advance(48 * time.Hour)
	if !e.Clock.Now().After(before) {
		t.Fatal("fake clock did not advance")
	}
}
