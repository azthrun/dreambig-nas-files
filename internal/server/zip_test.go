package server_test

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func zipEnv(t *testing.T) *testenv.Env {
	t.Helper()
	e := testenv.New(t,
		testenv.WithUser("bob"), testenv.Mount("docs"), testenv.Mount("secret"),
		testenv.Grant("alice", "docs", "write"), testenv.Grant("alice", "secret", "write"),
		testenv.Grant("bob", "docs", "read"),
	)
	e.WriteFile("docs/proj/a.txt", "aaa")
	e.WriteFile("docs/proj/sub/b.txt", "bbbb")
	e.WriteFile("docs/proj/文档/报告 – 终稿.txt", "中文内容")
	e.Mkdir("docs/proj/empty")
	e.WriteFile("docs/other.txt", "o")
	e.WriteFile("secret/s.txt", "top")
	return e
}

// readZip parses a response body as a zip and returns name -> content ("" for folders).
func readZip(t *testing.T, rec *httptest.ResponseRecorder) (map[string]string, *zip.Reader) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	b := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(data)
	}
	return out, zr
}

func names(m map[string]string) []string {
	var n []string
	for k := range m {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func TestFolderZipIsStoreOnlyWithUTF8Names(t *testing.T) {
	e := zipEnv(t)
	rec := e.SignIn("bob").Get("/files/docs/proj?zip=1")
	got, zr := readZip(t, rec)
	want := []string{"proj/", "proj/a.txt", "proj/empty/", "proj/sub/", "proj/sub/b.txt", "proj/文档/", "proj/文档/报告 – 终稿.txt"}
	if strings.Join(names(got), "|") != strings.Join(want, "|") {
		t.Errorf("entries = %q", names(got))
	}
	if got["proj/sub/b.txt"] != "bbbb" || got["proj/文档/报告 – 终稿.txt"] != "中文内容" {
		t.Errorf("content wrong: %v", got)
	}
	for _, f := range zr.File {
		if f.Method != zip.Store {
			t.Errorf("%s method = %d, want store", f.Name, f.Method)
		}
		if f.Flags&0x800 == 0 && strings.ContainsAny(f.Name, "文–") {
			t.Errorf("%s lacks the UTF-8 flag", f.Name)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "proj.zip") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !strings.Contains(e.Audit.String(), `"operation":"zip"`) || !strings.Contains(e.Audit.String(), `"path":"docs/proj"`) {
		t.Errorf("audit = %s", e.Audit.String())
	}
}

func TestSelectionZipsOnlyTheChosenItems(t *testing.T) {
	e := zipEnv(t)
	c := e.SignIn("bob") // read Access may select
	rec := c.Post("/files/docs/proj", url.Values{"action": {"zip"}, "names": {"a.txt", "sub"}})
	got, _ := readZip(t, rec)
	if strings.Join(names(got), "|") != "a.txt|sub/|sub/b.txt" {
		t.Errorf("entries = %q", names(got))
	}
	if rec := c.PostWithoutCSRF("/files/docs/proj", url.Values{"action": {"zip"}, "names": {"a.txt"}}); rec.Code != 403 {
		t.Errorf("no CSRF = %d", rec.Code)
	}
	if rec := c.Post("/files/docs/proj", url.Values{"action": {"zip"}}); rec.Code != 400 {
		t.Errorf("empty selection = %d", rec.Code)
	}
}
