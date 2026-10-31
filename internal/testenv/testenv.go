// Package testenv builds the whole application around temp folders for black-box
// httptest tests (the primary test seam). It is test support only; production code must
// not import it.
package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/identity"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/server"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
	"github.com/azthrun/dreambig-nas-files/internal/upload"
)

// Env is one running application with its temp Mounts.
type Env struct {
	// Dir is the temp data directory; Mount "docs" lives at Dir/docs.
	Dir    string
	Mounts []mounts.Mount
	Clock  *clock.Fake
	Audit  *bytes.Buffer
	// Trash is the Trash the app uses; PurgeTrash runs a purge with the fake clock.
	Trash *trash.Trash
	// Uploads is the Upload Keeper the app uses; ExpireUploads runs a sweep with the fake clock.
	Uploads *upload.Keeper
	Handler http.Handler
	users   map[string]string // name -> password
	t       testing.TB
}

// DefaultUser is a User every Env has.
const DefaultUser = "alice"

// Password returns the test password of a User of this Env.
func (e *Env) Password(name string) string {
	pw, ok := e.users[name]
	if !ok {
		e.t.Fatalf("testenv: no User %q", name)
	}
	return pw
}

// Option adjusts the environment before the app starts.
type Option func(*setup)

type setup struct {
	mounts    []string
	readOnly  map[string]bool
	users     []string
	grants    map[string]map[string]string // user -> mount -> level
	rawAcc    *string                      // AccessFile content
	trashDays *int
	expiry    *time.Duration
	reserve   int64
	freeSpace func(mount string) int64
}

// UploadExpiry sets how long an incomplete Upload may idle before the sweep removes it
// (default 7 days; 0 never).
func UploadExpiry(d time.Duration) Option {
	return func(s *setup) { s.expiry = &d }
}

// FreeSpaceReserve sets the free-space reserve in bytes (default 0: no check).
func FreeSpaceReserve(n int64) Option {
	return func(s *setup) { s.reserve = n }
}

// FreeSpace replaces the free-space probe: fn gets a Mount's name and returns the bytes
// free on its filesystem.
func FreeSpace(fn func(mount string) int64) Option {
	return func(s *setup) { s.freeSpace = fn }
}

// TrashRetention sets the Trash retention in days (default 30; 0 means never purge).
func TrashRetention(days int) Option {
	return func(s *setup) { s.trashDays = &days }
}

// Grant gives a User an Access level ("none", "read" or "write") in a Mount, as if the
// owner had written it in the Access file. The first Grant (or AccessFile) turns off the
// default, which is DefaultUser with write Access to every Mount; Users not granted
// anything have none.
func Grant(user, mount, level string) Option {
	return func(s *setup) {
		if s.grants[user] == nil {
			s.grants[user] = map[string]string{}
		}
		s.grants[user][mount] = level
	}
}

// AccessFile replaces the Access file content with raw text, for startup-refusal tests
// (use TryNew).
func AccessFile(content string) Option {
	return func(s *setup) { s.rawAcc = &content }
}

// WithUser adds a User (besides DefaultUser) who can sign in. Their password is
// Env.Password(name).
func WithUser(name string) Option {
	return func(s *setup) { s.users = append(s.users, name) }
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
	s := &setup{readOnly: map[string]bool{}, grants: map[string]map[string]string{}}
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
	// Passwords are built at runtime so no password-looking literal sits in the repo.
	e.users = map[string]string{}
	var htpasswd strings.Builder
	for _, n := range append([]string{DefaultUser}, s.users...) {
		pw := strings.Repeat(n[:1], 3) + "-test-" + strings.Repeat("pw", 4)
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		e.users[n] = pw
		htpasswd.WriteString(n + ":" + string(hash) + "\n")
	}
	auth, err := identity.Parse(strings.NewReader(htpasswd.String()))
	if err != nil {
		return nil, err
	}
	accessJSON := ""
	switch {
	case s.rawAcc != nil:
		accessJSON = *s.rawAcc
	default:
		grants := s.grants
		if len(grants) == 0 {
			grants = map[string]map[string]string{DefaultUser: {}}
			for _, m := range e.Mounts {
				grants[DefaultUser][m.Name] = "write"
			}
		}
		b, err := json.Marshal(grants)
		if err != nil {
			t.Fatal(err)
		}
		accessJSON = string(b)
	}
	names := auth.Names()
	acc, err := access.Parse(strings.NewReader(accessJSON), names, e.Mounts)
	if err != nil {
		return nil, err
	}
	days := 30
	if s.trashDays != nil {
		days = *s.trashDays
	}
	tr, err := trash.New(trash.Config{
		Mounts: e.Mounts, Clock: e.Clock, RetentionDays: days,
		Audit: audit.New(e.Audit, e.Clock),
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = tr.Close() })
	e.Trash = tr
	expiry := 7 * 24 * time.Hour
	if s.expiry != nil {
		expiry = *s.expiry
	}
	kc := upload.KeeperConfig{
		Mounts: e.Mounts, Clock: e.Clock, Expiry: expiry, Reserve: s.reserve,
		Audit: audit.New(e.Audit, e.Clock),
	}
	if s.freeSpace != nil {
		kc.FreeSpace = func(m mounts.Mount) (int64, error) { return s.freeSpace(m.Name), nil }
	}
	e.Uploads = upload.NewKeeper(kc)
	h, err := server.New(server.Deps{Mounts: e.Mounts, Clock: e.Clock, Audit: e.Audit, Auth: auth, Access: acc, Trash: tr, Uploads: e.Uploads})
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

// Symlink creates a symbolic link at rel (relative to Dir) pointing at target, which is
// written as given (absolute, or relative to the link's folder).
func (e *Env) Symlink(rel, target string) {
	e.t.Helper()
	p := filepath.Join(e.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		e.t.Fatal(err)
	}
}

// Chtimes sets the modification time of the file or folder at rel.
func (e *Env) Chtimes(rel string, mtime time.Time) {
	e.t.Helper()
	if err := os.Chtimes(filepath.Join(e.Dir, filepath.FromSlash(rel)), mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

// PurgeTrash runs one Trash purge now, by the fake clock, as the background loop would.
func (e *Env) PurgeTrash() { e.Trash.Purge() }

// ExpireUploads runs one Upload expiry sweep now, by the fake clock, as the background
// loop would.
func (e *Env) ExpireUploads() { e.Uploads.Sweep(context.Background()) }
