package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func upEnv(t *testing.T) *testenv.Env {
	t.Helper()
	e := testenv.New(t, testenv.WithUser("bob"),
		testenv.Mount("docs"), testenv.Mount("other"), testenv.ReadOnlyMount("archive"),
		testenv.Grant("alice", "docs", "write"), testenv.Grant("alice", "other", "write"),
		testenv.Grant("alice", "archive", "write"),
		testenv.Grant("bob", "docs", "write"), testenv.Grant("bob", "archive", "read"),
	)
	e.Mkdir("docs/sub")
	return e
}

func TestUploadResumesAfterADroppedConnection(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/big.bin", 10))
	tusPatch(c, loc, 0, "01234")
	// The connection dropped; the client asks where the server got to.
	head := tusHead(c, loc)
	if head.Code != http.StatusOK || head.Header().Get("Upload-Offset") != "5" {
		t.Fatalf("HEAD = %d offset %q", head.Code, head.Header().Get("Upload-Offset"))
	}
	if rec := tusPatch(c, loc, 5, "56789"); rec.Code != http.StatusNoContent {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body)
	}
	if got := e.ReadFile("docs/big.bin"); got != "0123456789" {
		t.Fatalf("file = %q", got)
	}
}

func TestCancelDiscardsStagedData(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/c.txt", 10))
	tusPatch(c, loc, 0, "abc")
	if rec := c.Do(tusReq(c, http.MethodDelete, loc, "", nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d: %s", rec.Code, rec.Body)
	}
	if !stagingEmpty(e, "docs") || exists(e, "docs/c.txt") {
		t.Error("cancel left data behind")
	}
	if rec := tusHead(c, loc); rec.Code != http.StatusNotFound {
		t.Errorf("cancelled Upload still answers HEAD with %d", rec.Code)
	}
}

func TestUploadToAnExistingPathIsRefusedAndNeverOverwrites(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/a.txt", "original")
	c := e.SignIn("alice")
	if rec := tusCreate(c, "docs/a.txt", 3); rec.Code != http.StatusConflict {
		t.Fatalf("create over existing = %d", rec.Code)
	}
	if got := e.ReadFile("docs/a.txt"); got != "original" {
		t.Fatalf("file = %q", got)
	}
}

func TestLateConflictIsRefusedAndNeverOverwrites(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/late.txt", 6))
	tusPatch(c, loc, 0, "new")
	e.WriteFile("docs/late.txt", "someone else") // appears while the Upload is in flight
	if rec := tusPatch(c, loc, 3, "new"); rec.Code != http.StatusConflict {
		t.Fatalf("last PATCH = %d", rec.Code)
	}
	if got := e.ReadFile("docs/late.txt"); got != "someone else" {
		t.Fatalf("file = %q", got)
	}
	// The data waits for the User to choose (see upload_conflict_test.go), but the Upload
	// must not look resumable-and-complete to the client meanwhile.
	if stagingEmpty(e, "docs") {
		t.Error("staged data was discarded before the User could choose")
	}
	if rec := tusHead(c, loc); rec.Code != http.StatusNotFound {
		t.Errorf("a refused Upload can still be resumed: HEAD = %d", rec.Code)
	}
}

func TestEveryTusRequestNeedsSessionAndCSRF(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/s.txt", 4))
	tusPatch(c, loc, 0, "ab")

	anon := e.Anonymous()
	noToken := e.SignIn("alice")
	noToken.CSRF = ""
	badToken := e.SignIn("alice")
	badToken.CSRF = "wrong"
	for name, cl := range map[string]*testenv.Client{"anonymous": anon, "no token": noToken, "wrong token": badToken} {
		want := http.StatusForbidden
		if cl == anon {
			want = http.StatusUnauthorized
		}
		if rec := tusCreate(cl, "docs/x.txt", 1); rec.Code != want {
			t.Errorf("%s create = %d, want %d", name, rec.Code, want)
		}
		if rec := tusPatch(cl, loc, 2, "cd"); rec.Code != want {
			t.Errorf("%s PATCH = %d, want %d", name, rec.Code, want)
		}
		if rec := cl.Do(tusReq(cl, http.MethodDelete, loc, "", nil)); rec.Code != want {
			t.Errorf("%s DELETE = %d, want %d", name, rec.Code, want)
		}
	}
	if rec := tusHead(anon, loc); rec.Code == http.StatusOK {
		t.Error("anonymous HEAD reads an Upload")
	}
	if head := tusHead(c, loc); head.Header().Get("Upload-Offset") != "2" {
		t.Errorf("Upload was changed: offset %q", head.Header().Get("Upload-Offset"))
	}
	if exists(e, "docs/x.txt") {
		t.Error("a refused create left a file")
	}
}

func TestAMethodOverrideCannotSmuggleAGetPastCSRF(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/o.txt", 4))
	noToken := e.SignIn("alice")
	noToken.CSRF = ""
	r := tusReq(noToken, http.MethodGet, loc, "ab", map[string]string{"X-HTTP-Method-Override": "PATCH", "Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"})
	if rec := noToken.Do(r); rec.Code == http.StatusNoContent {
		t.Fatal("PATCH through a GET override was accepted without a CSRF token")
	}
	if head := tusHead(c, loc); head.Header().Get("Upload-Offset") != "0" {
		t.Error("data was written")
	}
}

func TestUploadNeedsWriteAccess(t *testing.T) {
	e := upEnv(t)
	bob := e.SignIn("bob")
	if rec := tusCreate(bob, "archive/x.txt", 1); rec.Code != http.StatusForbidden {
		t.Errorf("read Access create = %d, want 403", rec.Code)
	}
	if rec := tusCreate(bob, "other/x.txt", 1); rec.Code != http.StatusNotFound {
		t.Errorf("no Access create = %d, want 404", rec.Code)
	}
	if rec := tusCreate(bob, "nonexistent/x.txt", 1); rec.Code != http.StatusNotFound {
		t.Errorf("unknown Mount create = %d, want 404", rec.Code)
	}
	// A read-only Mount refuses every User, even one granted write.
	if rec := tusCreate(e.SignIn("alice"), "archive/x.txt", 1); rec.Code != http.StatusForbidden {
		t.Errorf("read-only Mount create = %d, want 403", rec.Code)
	}
	if exists(e, "archive/.nas-files-staging") {
		t.Error("a read-only Mount got a Staging Area")
	}
	// The Root and a Mount folder themselves are read-only.
	for _, p := range []string{"docs", "newmount"} {
		if rec := tusCreate(e.SignIn("alice"), p, 1); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("create at %q = %d", p, rec.Code)
		}
	}
}

func TestNobodyElseCanReadOrChangeAnUpload(t *testing.T) {
	e := upEnv(t)
	alice, bob := e.SignIn("alice"), e.SignIn("bob")
	loc := uploadURL(t, tusCreate(alice, "docs/mine.txt", 6))
	tusPatch(alice, loc, 0, "abc")
	if rec := tusHead(bob, loc); rec.Code != http.StatusNotFound || rec.Header().Get("Upload-Offset") != "" {
		t.Errorf("bob HEAD = %d", rec.Code)
	}
	if rec := tusPatch(bob, loc, 3, "xyz"); rec.Code != http.StatusNotFound {
		t.Errorf("bob PATCH = %d", rec.Code)
	}
	if rec := bob.Do(tusReq(bob, http.MethodDelete, loc, "", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("bob DELETE = %d", rec.Code)
	}
	if rec := tusPatch(alice, loc, 3, "def"); rec.Code != http.StatusNoContent {
		t.Fatalf("alice PATCH = %d", rec.Code)
	}
	if got := e.ReadFile("docs/mine.txt"); got != "abcdef" {
		t.Fatalf("file = %q", got)
	}
	// Bob cannot take over by claiming alice's name or another Mount in the metadata.
	rec := bob.Do(tusReq(bob, http.MethodPost, "/uploads/", "", map[string]string{
		"Upload-Length":   "1",
		"Upload-Metadata": tusMeta(map[string]string{"path": "docs/b.txt", "user": "alice", "mount": "other"}),
	}))
	bobLoc := uploadURL(t, rec)
	if rec := tusHead(alice, bobLoc); rec.Code != http.StatusNotFound {
		t.Errorf("alice sees bob's Upload with a forged owner: %d", rec.Code)
	}
	if rec := tusPatch(bob, bobLoc, 0, "z"); rec.Code != http.StatusNoContent || e.ReadFile("docs/b.txt") != "z" {
		t.Errorf("bob's own Upload = %d", rec.Code)
	}
}

func TestUploadDestinationsAreValidatedByTheResolver(t *testing.T) {
	e := upEnv(t)
	e.Symlink("docs/out", t.TempDir())
	e.Symlink("docs/toarchive", "../archive")
	e.Symlink("docs/toother", "../other")
	e.WriteFile("docs/file.txt", "f")
	c := e.SignIn("alice")
	for _, tc := range []struct {
		path string
		want int
	}{
		{"docs/../other/x.txt", 400},
		{"../x.txt", 400},
		{"/docs/x.txt", 400},
		{"docs//x.txt", 400},
		{`docs\x.txt`, 400},
		{"docs/./x.txt", 400},
		{"docs/a:b.txt", 400},
		{"docs/q?.txt", 400},
		{"docs/trail.", 400},
		{"docs/nul.txt", 400},
		{"", 400},
		{"docs/@eaDir/x.txt", 404},
		{"docs/#recycle/x.txt", 404},
		{"docs/.nas-files-staging/x", 404},
		{"docs/.nas-files-trash/x", 404},
		{"docs/.DS_Store", 400},
		{"docs/out/x.txt", 404},       // symlink out of every Mount
		{"docs/toarchive/x.txt", 403}, // symlink into a read-only Mount
		{"docs/missing/x.txt", 201},   // missing parents are created at completion
		{"docs/file.txt/x.txt", 409},  // a file stands where a folder is needed
		{"docs/file.txt", 409},        // exists
		{"docs/sub", 409},             // a folder stands there
		{"docs/ok.txt", 201},
		{"docs/sub/ok.txt", 201},
		{"docs/toother/ok.txt", 201}, // symlink into another writable Mount lands there
	} {
		if rec := tusCreate(c, tc.path, 1); rec.Code != tc.want {
			t.Errorf("create %q = %d, want %d: %s", tc.path, rec.Code, tc.want, rec.Body)
		}
	}
}

func TestUploadThroughALinkStagesOnTheTargetMountsFilesystem(t *testing.T) {
	e := upEnv(t)
	e.Symlink("docs/lnk", "../other")
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/lnk/via.txt", 2))
	if exists(e, "docs/.nas-files-staging") {
		t.Error("staged in the Mount the Path was typed under, not the real one")
	}
	tusPatch(c, loc, 0, "hi")
	if got := e.ReadFile("other/via.txt"); got != "hi" {
		t.Fatalf("file = %q", got)
	}
}

func TestPartialDataIsNeverListedOrDownloadable(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/part.txt", 10))
	tusPatch(c, loc, 0, "secret-partial")
	if body := c.Get("/files/docs").Body.String(); strings.Contains(body, "part.txt") || strings.Contains(body, "nas-files-staging") {
		t.Errorf("listing shows the Upload:\n%s", body)
	}
	if rec := c.Get("/files/docs/part.txt"); rec.Code != http.StatusNotFound {
		t.Errorf("download of the Path = %d", rec.Code)
	}
	id := strings.TrimPrefix(loc, "/uploads/")
	for _, p := range []string{"/files/docs/.nas-files-staging", "/files/docs/.nas-files-staging/" + id} {
		if rec := c.Get(p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
	if rec := c.Get(loc); rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "partial") {
		t.Errorf("GET of the Upload URL = %d", rec.Code)
	}
}

func TestZeroLengthUploadCreatesAnEmptyFile(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	if rec := tusCreate(c, "docs/empty.txt", 0); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	if !exists(e, "docs/empty.txt") || e.ReadFile("docs/empty.txt") != "" {
		t.Error("no empty file")
	}
}

func TestUploadsAreAudited(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/aud.txt", 4))
	tusPatch(c, loc, 0, "abcd")
	tusCreate(e.SignIn("bob"), "archive/no.txt", 1)
	tusCreate(c, "../escape", 1)
	var ops []string
	for _, l := range auditLines(t, e) {
		op, _ := l["operation"].(string)
		if !strings.HasPrefix(op, "upload") {
			continue
		}
		ops = append(ops, op+":"+str(l["outcome"])+":"+str(l["user"])+":"+str(l["path"]))
		if op == "upload" && l["bytes"] != float64(4) {
			t.Errorf("completion line lacks bytes: %v", l)
		}
	}
	want := []string{
		"upload_create:ok:alice:docs/aud.txt",
		"upload:ok:alice:docs/aud.txt",
		"upload_create:refused:bob:archive/no.txt",
		"upload_create:refused:alice:",
	}
	if strings.Join(ops, "|") != strings.Join(want, "|") {
		t.Errorf("audit = %v\nwant    %v", ops, want)
	}
}

func str(v any) string { s, _ := v.(string); return s }
