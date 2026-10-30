// Package testenv builds the whole application around temp folders for black-box
// httptest tests (the primary test seam). It is test support only; production code must
// not import it.
package testenv

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/server"
)

// Env is one running application with its temp Mounts.
type Env struct {
	// Dir is the temp data directory; Mount "docs" lives at Dir/docs.
	Dir     string
	Mounts  []mounts.Mount
	Clock   *clock.Fake
	Audit   *bytes.Buffer
	Handler http.Handler
	t       testing.TB
}

// Option adjusts the environment before the app starts.
type Option func(*setup)

type setup struct {
	mounts   []string
	readOnly map[string]bool
}

// Mount adds a writable Mount folder with the given name.
func Mount(name string) Option {
	return func(s *setup) { s.mounts = append(s.mounts, name) }
}

// ReadOnlyMount adds a Mount that the service treats as read-only (as if mounted :ro).
// The folder itself stays writable on disk so tests can seed it.
func ReadOnlyMount(name string) Option {
	return func(s *setup) {
		s.mounts = append(s.mounts, name)
		s.readOnly[name] = true
	}
}

// Start is the fixed time of the fake clock.
var Start = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

// New starts the application and fails the test if it cannot.
func New(t testing.TB, opts ...Option) *Env {
	t.Helper()
	e, err := TryNew(t, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TryNew is New but returns the startup error, for tests of startup refusals.
func TryNew(t testing.TB, opts ...Option) (*Env, error) {
	t.Helper()
	s := &setup{readOnly: map[string]bool{}}
	for _, o := range opts {
		o(s)
	}
	dir := t.TempDir()
	for _, n := range s.mounts {
		if err := os.Mkdir(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e := &Env{Dir: dir, Clock: clock.NewFake(Start), Audit: &bytes.Buffer{}, t: t}
	if len(s.mounts) > 0 {
		// Temp folders share one filesystem, so the mount-point rule is off.
		ms, err := mounts.Discover(dir, mounts.Options{})
		if err != nil {
			return nil, err
		}
		for i := range ms {
			ms[i].ReadOnly = ms[i].ReadOnly || s.readOnly[ms[i].Name]
		}
		e.Mounts = ms
	}
	h, err := server.New(server.Deps{Mounts: e.Mounts, Clock: e.Clock, Audit: e.Audit})
	if err != nil {
		return nil, err
	}
	e.Handler = h
	return e, nil
}

// Get performs an unauthenticated GET and returns the recorded response.
func (e *Env) Get(target string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.Do(httptest.NewRequest(http.MethodGet, target, nil))
}

// Do serves r and returns the recorded response.
func (e *Env) Do(r *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.Handler.ServeHTTP(rec, r)
	return rec
}

// Mkdir creates a folder (and parents) at a Mount-relative path on disk.
func (e *Env) Mkdir(rel string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Join(e.Dir, filepath.FromSlash(rel)), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

// WriteFile creates a file (and parents) at a Mount-relative path on disk.
func (e *Env) WriteFile(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// ReadFile returns a file's content from disk, for asserting side effects.
func (e *Env) ReadFile(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.Dir, filepath.FromSlash(rel)))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}
