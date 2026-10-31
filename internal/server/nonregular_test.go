package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// noHang runs a request that must not wait for a writer on fifo.
func noHang(t *testing.T, fifo string, request func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request() }()
	select {
	case rec := <-done:
		return rec
	case <-time.After(5 * time.Second):
		// Let the stuck open finish so the test can end.
		if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		t.Fatal("the request hangs on the FIFO")
		return nil
	}
}

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
		return noHang(t, fifo, func() *httptest.ResponseRecorder { return c.Do(r) })
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

// Every form posted to a Path treats it as a folder first; a User with only read Access
// must not be able to park a handler on a FIFO that way.
func TestPostingToAFIFOIsRefusedWithoutHanging(t *testing.T) {
	e := testenv.New(t, testenv.WithUser("bob"), testenv.Mount("docs"), testenv.Grant("bob", "docs", "read"))
	fifo := filepath.Join(e.Dir, "docs", "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	e.Symlink("docs/link", "pipe")
	c := e.SignIn("bob")

	for _, target := range []string{"/files/docs/pipe", "/files/docs/link"} {
		for _, op := range []string{"delete", "delete-confirm", "zip", "mkdir"} {
			form := url.Values{"op": {op}, "names": {"x"}, "name": {"x"}}
			rec := noHang(t, fifo, func() *httptest.ResponseRecorder { return c.Post(target, form) })
			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("%s on %s = %d, want a refusal", op, target, rec.Code)
			}
		}
	}
	if ls := refusalsOf(t, e, "delete"); len(ls) != 4 || ls[0]["path"] != "docs/pipe" || ls[0]["status"] != float64(http.StatusNotFound) {
		t.Errorf("want each delete refused as a miss and audited, got %v", ls)
	}
	if !exists(e, "docs/pipe") {
		t.Error("the FIFO is gone")
	}
}

// makeFIFO adds a FIFO and a link to it to docs/proj of zipEnv.
func makeFIFO(t *testing.T, e *testenv.Env) {
	t.Helper()
	if err := syscall.Mkfifo(filepath.Join(e.Dir, "docs", "proj", "pipe"), 0o644); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	e.Symlink("docs/proj/link", "pipe")
}

func TestZippingOnlyAFIFOIsRefused(t *testing.T) {
	e := zipEnv(t)
	makeFIFO(t, e)
	rec := e.SignIn("bob").Post("/files/docs/proj", url.Values{"op": {"zip"}, "names": {"pipe", "link"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("zip = %d, want a 404", rec.Code)
	}
	ls := refusalsOf(t, e, "zip")
	if len(ls) != 2 || ls[0]["path"] != "docs/proj/pipe" || ls[1]["path"] != "docs/proj/link" {
		t.Errorf("want one refusal per name, got %v", ls)
	}
	for _, l := range auditLines(t, e) {
		if l["operation"] == "zip" && l["outcome"] == "ok" {
			t.Errorf("audited as a good zip: %v", l)
		}
	}
}

func TestASelectedFIFOIsDroppedFromTheZipAndAudited(t *testing.T) {
	e := zipEnv(t)
	makeFIFO(t, e)
	got, _ := readZip(t, e.SignIn("bob").Post("/files/docs/proj", url.Values{"op": {"zip"}, "names": {"pipe", "a.txt"}}))
	if got["a.txt"] != "aaa" {
		t.Errorf("zip holds %v", names(got))
	}
	if _, ok := got["_incomplete.txt"]; ok {
		t.Error("a dropped name is not an incomplete zip")
	}
	if ls := refusalsOf(t, e, "zip"); len(ls) != 1 || ls[0]["path"] != "docs/proj/pipe" {
		t.Errorf("want the drop refused, got %v", ls)
	}
	ev := lastAuditOf(t, e, "zip")
	if items, _ := ev["items"].([]any); ev["outcome"] != "ok" || len(items) != 1 || items[0] != "docs/proj/a.txt" {
		t.Errorf("audit = %v", ev)
	}
}

func TestAFIFOInsideAZippedFolderIsListedAsLeftOut(t *testing.T) {
	e := zipEnv(t)
	makeFIFO(t, e)
	got, _ := readZip(t, e.SignIn("bob").Get("/files/docs/proj?zip=1"))
	note := got["_incomplete.txt"]
	for _, want := range []string{"docs/proj/pipe", "docs/proj/link"} {
		if !strings.Contains(note, want) {
			t.Errorf("note does not list %s: %q", want, note)
		}
	}
	if got["proj/a.txt"] != "aaa" {
		t.Error("the rest of the zip was not streamed")
	}
	if ev := zipAuditLine(t, e.Audit.String()); ev["outcome"] != "partial" || ev["omitted"] != float64(2) {
		t.Errorf("audit = %v", ev)
	}
}
