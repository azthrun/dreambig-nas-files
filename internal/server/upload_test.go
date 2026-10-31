package server_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// tusMeta encodes tus Upload-Metadata.
func tusMeta(kv map[string]string) string {
	var parts []string
	for k, v := range kv {
		parts = append(parts, k+" "+base64.StdEncoding.EncodeToString([]byte(v)))
	}
	return strings.Join(parts, ",")
}

// tusReq builds a tus request carrying this client's CSRF token (as tus-js-client does).
func tusReq(c *testenv.Client, method, target string, body string, h map[string]string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Tus-Resumable", "1.0.0")
	if c.CSRF != "" {
		r.Header.Set("X-CSRF-Token", c.CSRF)
	}
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return r
}

// tusCreate starts an Upload of size bytes to path and returns the response.
func tusCreate(c *testenv.Client, path string, size int) *httptest.ResponseRecorder {
	return c.Do(tusReq(c, http.MethodPost, "/uploads/", "", map[string]string{
		"Upload-Length":   strconv.Itoa(size),
		"Upload-Metadata": tusMeta(map[string]string{"path": path}),
	}))
}

// tusPatch sends body at offset to an Upload URL (the Location of tusCreate).
func tusPatch(c *testenv.Client, loc string, offset int, body string) *httptest.ResponseRecorder {
	return c.Do(tusReq(c, http.MethodPatch, loc, body, map[string]string{
		"Upload-Offset": strconv.Itoa(offset),
		"Content-Type":  "application/offset+octet-stream",
	}))
}

func tusHead(c *testenv.Client, loc string) *httptest.ResponseRecorder {
	return c.Do(tusReq(c, http.MethodHead, loc, "", nil))
}

// uploadURL returns the path of the Upload a successful create answered with.
func uploadURL(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	loc := rec.Header().Get("Location")
	i := strings.Index(loc, "/uploads/")
	if i < 0 {
		t.Fatalf("Location %q is not an Upload URL", loc)
	}
	return loc[i:]
}

// stagingEmpty reports that a Mount's Staging Area holds no Upload data.
func stagingEmpty(e *testenv.Env, mount string) bool {
	des, err := os.ReadDir(filepath.Join(e.Dir, mount, ".nas-files-staging"))
	return err != nil || len(des) == 0
}

func TestUploadAppearsOnlyWhenComplete(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)

	loc := uploadURL(t, tusCreate(c, "docs/a.txt", 10))
	if rec := tusPatch(c, loc, 0, "hello"); rec.Code != http.StatusNoContent {
		t.Fatalf("first PATCH = %d: %s", rec.Code, rec.Body)
	}
	if exists(e, "docs/a.txt") {
		t.Fatal("partial data is already at its Path")
	}
	if rec := tusPatch(c, loc, 5, "world"); rec.Code != http.StatusNoContent {
		t.Fatalf("last PATCH = %d: %s", rec.Code, rec.Body)
	}
	if got := e.ReadFile("docs/a.txt"); got != "helloworld" {
		t.Fatalf("file = %q", got)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind after completion")
	}
}
