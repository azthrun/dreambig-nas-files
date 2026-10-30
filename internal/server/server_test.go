package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
