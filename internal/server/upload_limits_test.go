package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

const day = 24 * time.Hour

func stagingNames(e *testenv.Env, mount string) []string {
	des, _ := os.ReadDir(filepath.Join(e.Dir, mount, ".nas-files-staging"))
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

func auditOps(t *testing.T, e *testenv.Env, op string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range auditLines(t, e) {
		if l["operation"] == op {
			out = append(out, l)
		}
	}
	return out
}

func TestAbandonedUploadIsRemovedAfterSevenDaysAndAudited(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	loc := uploadURL(t, tusCreate(c, "docs/a.bin", 10))
	if rec := tusPatch(c, loc, 0, "abcd"); rec.Code != http.StatusNoContent {
		t.Fatalf("patch = %d", rec.Code)
	}

	e.Clock.Advance(7*day - time.Minute)
	e.ExpireUploads()
	if rec := tusHead(c, loc); rec.Code != http.StatusOK {
		t.Fatalf("Upload gone before 7 days: HEAD = %d", rec.Code)
	}

	e.Clock.Advance(2 * time.Minute)
	e.ExpireUploads()
	if rec := tusHead(c, loc); rec.Code != http.StatusNotFound {
		t.Errorf("Upload survived 7 days: HEAD = %d", rec.Code)
	}
	if !stagingEmpty(e, "docs") {
		t.Errorf("staged data left: %v", stagingNames(e, "docs"))
	}
	lines := auditOps(t, e, "upload_expire")
	if len(lines) != 1 {
		t.Fatalf("want one upload_expire audit line, got %v", lines)
	}
	l := lines[0]
	if l["user"] != "alice" || l["path"] != "docs/a.bin" || l["outcome"] != "ok" || l["bytes"] != float64(4) {
		t.Errorf("audit line = %v", l)
	}
}

func TestExpiryIsMeasuredFromLastActivity(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	loc := uploadURL(t, tusCreate(c, "docs/a.bin", 10))
	e.Clock.Advance(5 * day)
	if rec := tusPatch(c, loc, 0, "abcd"); rec.Code != http.StatusNoContent {
		t.Fatalf("patch = %d", rec.Code)
	}
	e.Clock.Advance(5 * day) // 10 days since creation, 5 since the last bytes
	e.ExpireUploads()
	if rec := tusHead(c, loc); rec.Code != http.StatusOK {
		t.Fatalf("active Upload expired: HEAD = %d", rec.Code)
	}
	e.Clock.Advance(3 * day) // 8 days idle
	e.ExpireUploads()
	if rec := tusHead(c, loc); rec.Code != http.StatusNotFound {
		t.Errorf("idle Upload survived: HEAD = %d", rec.Code)
	}
}

func TestExpirySweepsEveryMountAndLeavesOthersAlone(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("media"))
	c := e.SignIn(testenv.DefaultUser)
	uploadURL(t, tusCreate(c, "docs/a.bin", 10))
	uploadURL(t, tusCreate(c, "media/b.bin", 10))
	// A finished Upload's file is not Staging Area data and must stay.
	done := uploadURL(t, tusCreate(c, "docs/done.txt", 3))
	tusPatch(c, done, 0, "xyz")
	// Left over from an earlier run: data with no info file, and an unrelated file.
	orphan := strings.Repeat("ab", 16)
	e.WriteFile("media/.nas-files-staging/"+orphan, "junk")
	e.Chtimes("media/.nas-files-staging/"+orphan, testenv.Start.Add(-30*day))
	e.WriteFile("media/.nas-files-staging/keep.txt", "not an Upload")
	e.Chtimes("media/.nas-files-staging/keep.txt", testenv.Start.Add(-30*day))

	e.Clock.Advance(8 * day)
	e.ExpireUploads()

	if got := stagingNames(e, "docs"); len(got) != 0 {
		t.Errorf("docs staging = %v", got)
	}
	if got := stagingNames(e, "media"); len(got) != 1 || got[0] != "keep.txt" {
		t.Errorf("media staging = %v, want only keep.txt", got)
	}
	if e.ReadFile("docs/done.txt") != "xyz" {
		t.Error("completed file damaged by expiry")
	}
}

func TestExpiryDisabledWithZeroDays(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"), testenv.UploadExpiry(0))
	c := e.SignIn(testenv.DefaultUser)
	loc := uploadURL(t, tusCreate(c, "docs/a.bin", 10))
	e.Clock.Advance(400 * day)
	e.ExpireUploads()
	c = e.SignIn(testenv.DefaultUser) // the session itself lapsed long ago
	if rec := tusHead(c, loc); rec.Code != http.StatusOK {
		t.Errorf("HEAD = %d, want Upload kept", rec.Code)
	}
}

func TestAnUploadBeingWrittenSurvivesASweep(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	loc := uploadURL(t, tusCreate(c, "docs/a.bin", 8))

	pr, pw := io.Pipe()
	r := tusReq(c, http.MethodPatch, loc, "", map[string]string{
		"Upload-Offset": "0", "Content-Type": "application/offset+octet-stream",
	})
	r.Body = io.NopCloser(pr)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- c.Do(r) }()
	if _, err := pw.Write([]byte("abcd")); err != nil { // returns once the handler has read it
		t.Fatal(err)
	}

	e.Clock.Advance(30 * day) // looks abandoned, but a request is writing to it
	e.ExpireUploads()
	if got := auditOps(t, e, "upload_expire"); len(got) != 0 {
		t.Errorf("in-flight Upload expired: %v", got)
	}

	if _, err := pw.Write([]byte("efgh")); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	if rec := <-done; rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d (cut off by the sweep?)", rec.Code)
	}
	if e.ReadFile("docs/a.bin") != "abcdefgh" {
		t.Errorf("file = %q", e.ReadFile("docs/a.bin"))
	}
}

func TestNewUploadIsRefusedWhenFreeSpaceWouldFallBelowTheReserve(t *testing.T) {
	free := map[string]int64{"docs": 1000, "media": 100000}
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("media"),
		testenv.FreeSpaceReserve(500),
		testenv.FreeSpace(func(mount string) int64 { return free[mount] }))
	c := e.SignIn(testenv.DefaultUser)

	// free (1000) >= reserve (500) + length (500): fits exactly.
	loc := uploadURL(t, tusCreate(c, "docs/fits.bin", 500))

	rec := tusCreate(c, "docs/big.bin", 501)
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("create = %d, want 507", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ERR_INSUFFICIENT_SPACE") {
		t.Errorf("body lacks the stable code: %q", rec.Body)
	}
	if len(stagingNames(e, "docs")) != 2 { // only fits.bin: data + info
		t.Errorf("refused Upload left staged data: %v", stagingNames(e, "docs"))
	}

	// The Mount's own filesystem counts: the same size fits in media.
	uploadURL(t, tusCreate(c, "media/big.bin", 501))

	lines := auditOps(t, e, "upload_create")
	var refused map[string]any
	for _, l := range lines {
		if l["outcome"] == "refused" && l["status"] == float64(507) {
			refused = l
		}
	}
	if refused == nil || refused["user"] != "alice" || refused["path"] != "docs/big.bin" {
		t.Errorf("refusal not audited: %v", lines)
	}

	// Only new Uploads are refused: one already started finishes.
	free["docs"] = 0
	if rec := tusPatch(c, loc, 0, strings.Repeat("x", 500)); rec.Code != http.StatusNoContent {
		t.Errorf("in-flight Upload refused: %d", rec.Code)
	}
	if rec := tusCreate(c, "docs/more.bin", 1); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("new Upload with no space = %d", rec.Code)
	}
}

func TestNoReserveMeansNoSpaceCheck(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"),
		testenv.FreeSpace(func(string) int64 { return 0 }))
	c := e.SignIn(testenv.DefaultUser)
	uploadURL(t, tusCreate(c, "docs/a.bin", 5))
}

func TestSpaceRefusalIsTranslatedOnTheFolderPage(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.SignIn(testenv.DefaultUser)
	en := c.Get("/files/docs").Body.String()
	c.AcceptLanguage = "zh"
	zh := c.Get("/files/docs").Body.String()
	msg := func(s string) string {
		const key = `data-msg-no_space="`
		i := strings.Index(s, key)
		if i < 0 {
			return ""
		}
		s = s[i+len(key):]
		return s[:strings.Index(s, `"`)]
	}
	if msg(en) == "" || msg(zh) == "" || msg(en) == msg(zh) {
		t.Errorf("no_space message en=%q zh=%q", msg(en), msg(zh))
	}
}

func TestUploadScriptShowsTheSpaceMessageWithoutRetrying(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	js := e.Anonymous().Get("/static/upload.js").Body.String()
	for _, want := range []string{"507", `msg("no_space")`, "onShouldRetry"} {
		if !strings.Contains(js, want) {
			t.Errorf("upload.js lacks %s", want)
		}
	}
}
