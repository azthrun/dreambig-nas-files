package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// A FIFO blocks whoever opens it until a writer comes. A request must not be that reader:
// such an entry is refused like a Path that is not there.
func TestAFIFOIsRefusedWithoutHanging(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	fifo := filepath.Join(e.Dir, "docs", "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	e.Symlink("docs/link", "pipe")
	c := e.SignIn(testenv.DefaultUser)
	want := c.Get("/files/docs/nonexistent").Body.String()

	do := func(r *http.Request) *httptest.ResponseRecorder {
		t.Helper()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- c.Do(r) }()
		select {
		case rec := <-done:
			return rec
		case <-time.After(5 * time.Second):
			// Let the stuck open finish so the test can end.
			if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
				f.Close()
			}
			t.Fatalf("%s %s hangs on the FIFO", r.Method, r.URL)
			return nil
		}
	}

	for _, target := range []string{"/files/docs/pipe", "/files/docs/link", "/files/docs/pipe?zip=1"} {
		if rec := do(httptest.NewRequest(http.MethodGet, target, nil)); rec.Code != http.StatusNotFound || rec.Body.String() != want {
			t.Errorf("GET %s = %d, want the same 404 as a missing Path", target, rec.Code)
		}
		if rec := do(httptest.NewRequest(http.MethodHead, target, nil)); rec.Code != http.StatusNotFound {
			t.Errorf("HEAD %s = %d", target, rec.Code)
		}
	}
	if ls := refusalsOf(t, e, "browse"); len(ls) != 6 || ls[0]["path"] != "docs/pipe" {
		t.Errorf("want each refusal audited, got %v", ls)
	}
	// A name that needs a folder scan to find its stored form must not open the FIFO either.
	for _, target := range []string{"/files/docs/pipe/", "/files/docs/link/"} {
		if rec := do(httptest.NewRequest(http.MethodGet, target+url.PathEscape("é"), nil)); rec.Code != http.StatusNotFound {
			t.Errorf("GET below %s = %d", target, rec.Code)
		}
	}
}
