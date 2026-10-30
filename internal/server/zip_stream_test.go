package server_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipStopsWhenTheClientDisconnects(t *testing.T) {
	e := zipEnv(t)
	e.WriteFile("docs/big/data.bin", strings.Repeat("x", 8<<20))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client is already gone
	req := httptest.NewRequest(http.MethodGet, "/files/docs/big?zip=1", nil).WithContext(ctx)
	rec := e.SignIn("alice").Do(req)
	if rec.Body.Len() > 1<<20 {
		t.Errorf("kept sending after disconnect: %d bytes", rec.Body.Len())
	}
	if !strings.Contains(e.Audit.String(), `"operation":"zip"`) || !strings.Contains(e.Audit.String(), `"outcome":"failed"`) {
		t.Errorf("audit = %s", e.Audit.String())
	}
}

func TestZipStartsStreamingBeforeReadingFiles(t *testing.T) {
	e := zipEnv(t)
	rec := e.SignIn("alice").Get("/files/docs/proj?zip=1")
	if !rec.Flushed {
		t.Error("headers were not flushed up front")
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Error("a streamed zip has no Content-Length")
	}
}

// tailWriter keeps the last bytes and a count, so a multi-GB response is never buffered.
type tailWriter struct {
	h    http.Header
	n    int64
	tail []byte
}

func (w *tailWriter) Header() http.Header { return w.h }
func (w *tailWriter) WriteHeader(int)     {}
func (w *tailWriter) Write(b []byte) (int, error) {
	w.n += int64(len(b))
	w.tail = append(w.tail, b...)
	if len(w.tail) > 8192 {
		w.tail = w.tail[len(w.tail)-4096:]
	}
	return len(b), nil
}

func TestZipUsesZip64ForFilesOverFourGB(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 4 GB of zeros")
	}
	e := zipEnv(t)
	p := filepath.Join(e.Dir, "docs", "huge.bin")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(4<<30 + 1); err != nil { // sparse
		t.Skipf("cannot make a sparse file here: %v", err)
	}
	f.Close()
	w := &tailWriter{h: http.Header{}}
	e.SignIn("alice").ServeTo(w, httptest.NewRequest(http.MethodGet, "/files/docs?zip=1", nil))
	if w.n < 4<<30 {
		t.Fatalf("only %d bytes streamed", w.n)
	}
	if !bytes.Contains(w.tail, []byte("PK\x06\x06")) {
		t.Error("no Zip64 end-of-central-directory record")
	}
}
