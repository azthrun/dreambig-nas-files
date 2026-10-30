package server_test

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func dlEnv(t *testing.T) *testenv.Env {
	t.Helper()
	e := testenv.New(t,
		testenv.WithUser("bob"), testenv.Mount("docs"), testenv.Mount("secret"),
		testenv.Grant("alice", "docs", "write"), testenv.Grant("alice", "secret", "write"),
		testenv.Grant("bob", "docs", "read"),
	)
	e.WriteFile("docs/a.txt", "0123456789")
	e.WriteFile("docs/文档/报告 – 终稿.txt", "中文内容")
	e.WriteFile("secret/s.txt", "top")
	return e
}

func TestDownloadSendsTheFileWithItsOriginalName(t *testing.T) {
	e := dlEnv(t)
	rec := e.SignIn("bob").Get("/files/docs/a.txt")
	if rec.Code != 200 || rec.Body.String() != "0123456789" {
		t.Fatalf("download = %d %q", rec.Code, rec.Body.String())
	}
	_, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") || params["filename"] != "a.txt" {
		t.Errorf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges = %q", rec.Header().Get("Accept-Ranges"))
	}
}

func TestDownloadPreservesNonASCIINames(t *testing.T) {
	e := dlEnv(t)
	rec := e.SignIn("alice").Get("/files/docs/%E6%96%87%E6%A1%A3/%E6%8A%A5%E5%91%8A%20%E2%80%93%20%E7%BB%88%E7%A8%BF.txt")
	if rec.Code != 200 || rec.Body.String() != "中文内容" {
		t.Fatalf("download = %d %q", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(cd)
	if err != nil || params["filename"] != "报告 – 终稿.txt" {
		t.Errorf("Content-Disposition %q parsed to %v (%v)", cd, params, err)
	}
	for _, r := range cd {
		if r > 126 {
			t.Errorf("header carries raw non-ASCII: %q", cd)
			break
		}
	}
}

func TestDownloadHonoursRangeRequests(t *testing.T) {
	e := dlEnv(t)
	c := e.SignIn("bob")
	req := httptest.NewRequest(http.MethodGet, "/files/docs/a.txt", nil)
	req.Header.Set("Range", "bytes=4-")
	rec := c.Do(req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "456789" || rec.Header().Get("Content-Range") != "bytes 4-9/10" {
		t.Errorf("range = %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Range"))
	}
	req = httptest.NewRequest(http.MethodGet, "/files/docs/a.txt", nil)
	req.Header.Set("Range", "bytes=50-")
	if rec := c.Do(req); rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("bad range = %d", rec.Code)
	}
}

func TestDownloadHeadSendsNoBody(t *testing.T) {
	e := dlEnv(t)
	rec := e.SignIn("bob").Do(httptest.NewRequest(http.MethodHead, "/files/docs/a.txt", nil))
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "10" {
		t.Errorf("HEAD = %d len %d CL %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
}

func TestDownloadIsNeverRenderedInline(t *testing.T) {
	e := dlEnv(t)
	e.WriteFile("docs/x.html", "<script>alert(1)</script>")
	rec := e.SignIn("bob").Get("/files/docs/x.html")
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers %v", rec.Header())
	}
}

func TestDownloadRefusesNoAccessHiddenAndEscapes(t *testing.T) {
	e := dlEnv(t)
	e.WriteFile("docs/@eaDir/h.txt", "hidden-content")
	e.WriteFile("docs/.DS_Store", "hidden-content")
	e.Symlink("docs/tosecret.txt", "../secret/s.txt")
	e.Symlink("docs/hid.txt", "@eaDir/h.txt")
	bob := e.SignIn("bob")
	missing := bob.Get("/files/docs/nope.txt")
	for _, p := range []string{
		"/files/secret/s.txt", "/files/docs/@eaDir/h.txt", "/files/docs/.DS_Store",
		"/files/docs/tosecret.txt", "/files/docs/hid.txt", "/files/docs/..%2Fsecret%2Fs.txt",
	} {
		rec := bob.Get(p)
		if rec.Code != 404 && rec.Code != 400 {
			t.Errorf("%s = %d", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "top") || strings.Contains(rec.Body.String(), "hidden-content") {
			t.Errorf("%s leaked content", p)
		}
	}
	if rec := bob.Get("/files/secret/s.txt"); rec.Code != missing.Code || rec.Body.String() != missing.Body.String() {
		t.Errorf("no-Access file differs from nonexistent: %d", rec.Code)
	}
}

func TestDownloadsAndRefusalsAreAudited(t *testing.T) {
	e := dlEnv(t)
	e.Mkdir("docs/@eaDir")
	bob := e.SignIn("bob")
	bob.Get("/files/docs/a.txt")
	bob.Get("/files/docs/@eaDir/x")
	var ok bool
	for _, l := range auditLines(t, e) {
		if l["operation"] == "download" && l["user"] == "bob" && l["path"] == "docs/a.txt" && l["outcome"] == "ok" && l["bytes"] == float64(10) {
			ok = true
		}
	}
	if !ok {
		t.Errorf("no download audit line:\n%s", e.Audit.String())
	}
}

func TestDownloadNeedsASession(t *testing.T) {
	e := dlEnv(t)
	if rec := e.Anonymous().Get("/files/docs/a.txt"); rec.Code != http.StatusSeeOther || strings.Contains(rec.Body.String(), "0123") {
		t.Errorf("anonymous = %d", rec.Code)
	}
}

func TestBrowseLinksFilesToDownload(t *testing.T) {
	e := dlEnv(t)
	body := e.SignIn("bob").Get("/files/docs").Body.String()
	if !strings.Contains(body, `href="/files/docs/a.txt"`) {
		t.Errorf("file not linked:\n%s", body)
	}
	want := "/files/docs/" + url.PathEscape("文档")
	if !strings.Contains(body, want) {
		t.Errorf("folder link missing %s", want)
	}
}
