package server_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// accessEnv: alice writes docs, reads archive (ro Mount); bob reads docs only;
// carol has no Access anywhere. "secret" is a Mount nobody but alice sees.
func accessEnv(t *testing.T) *testenv.Env {
	t.Helper()
	e := testenv.New(t,
		testenv.WithUser("bob"), testenv.WithUser("carol"),
		testenv.Mount("docs"), testenv.ReadOnlyMount("archive"), testenv.Mount("secret"),
		testenv.Grant("alice", "docs", "write"), testenv.Grant("alice", "archive", "write"),
		testenv.Grant("alice", "secret", "write"),
		testenv.Grant("bob", "docs", "read"),
	)
	e.WriteFile("docs/a.txt", "a")
	e.WriteFile("archive/old.txt", "o")
	e.WriteFile("secret/s.txt", "s")
	return e
}

func TestRootListsOnlyMountsTheUserHasAccessTo(t *testing.T) {
	e := accessEnv(t)
	body := e.SignIn("bob").Get("/").Body.String()
	if !strings.Contains(body, ">docs<") {
		t.Errorf("bob's Root lacks docs:\n%s", body)
	}
	for _, m := range []string{"archive", "secret"} {
		if strings.Contains(body, ">"+m+"<") {
			t.Errorf("bob's Root lists %s without Access", m)
		}
	}
	body = e.SignIn("carol").Get("/").Body.String()
	if strings.Contains(body, "/files/") || !strings.Contains(body, i18n.T(i18n.Default, "root.no_mounts")) {
		t.Errorf("carol (no Access) sees Mounts:\n%s", body)
	}
}

func TestMountWithoutAccessIsIndistinguishableFromANonexistentPath(t *testing.T) {
	e := accessEnv(t)
	bob := e.SignIn("bob")
	for _, c := range [][2]string{
		{"/files/secret", "/files/nonexistent"},
		{"/files/secret/s.txt", "/files/nonexistent/s.txt"},
		{"/files/archive", "/files/nonexistent"},
	} {
		got, want := bob.Get(c[0]), bob.Get(c[1])
		if got.Code != http.StatusNotFound || got.Code != want.Code || got.Body.String() != want.Body.String() {
			t.Errorf("GET %s = %d, differs from nonexistent (%d)", c[0], got.Code, want.Code)
		}
	}
	// Same inside an accessible Mount: no Access to the target is a plain miss.
	if rec := e.SignIn("carol").Get("/files/docs"); rec.Code != http.StatusNotFound {
		t.Errorf("carol GET /files/docs = %d, want 404", rec.Code)
	}
}

func TestAccessIsJudgedByTheRealTargetsMount(t *testing.T) {
	e := accessEnv(t)
	e.Symlink("docs/sneaky", "../secret") // a folder link out of docs into secret
	e.Symlink("docs/sfile", "../secret/s.txt")
	bob := e.SignIn("bob")
	if rec := bob.Get("/files/docs/sneaky"); rec.Code != http.StatusNotFound {
		t.Errorf("bob through symlink into secret = %d, want 404", rec.Code)
	}
	if rec := bob.Get("/files/docs"); strings.Contains(rec.Body.String(), "sneaky") || strings.Contains(rec.Body.String(), "sfile") {
		t.Errorf("bob's listing shows links into a Mount he cannot access")
	}
	// alice has Access to the target, so the link works and is listed.
	alice := e.SignIn("alice")
	if rec := alice.Get("/files/docs/sneaky"); rec.Code != http.StatusOK {
		t.Errorf("alice through symlink = %d, want 200", rec.Code)
	}
	if !strings.Contains(alice.Get("/files/docs").Body.String(), "sneaky") {
		t.Errorf("alice's listing lacks the link to a Mount she can access")
	}
}

func TestReadOnlyMountCapsWriteAccessAndWriteControlsFollowAccess(t *testing.T) {
	e := accessEnv(t)
	readOnlyTag := i18n.T(i18n.Default, "root.read_only")
	alice := e.SignIn("alice")
	if !strings.Contains(alice.Get("/files/archive").Body.String(), readOnlyTag) {
		t.Errorf("alice sees no read-only marker on a read-only Mount")
	}
	if strings.Contains(alice.Get("/files/docs").Body.String(), readOnlyTag) {
		t.Errorf("alice sees read-only marker on her writable Mount")
	}
	if !strings.Contains(e.SignIn("bob").Get("/files/docs").Body.String(), readOnlyTag) {
		t.Errorf("read-only User bob sees no read-only marker on docs")
	}
	if !strings.Contains(alice.Get("/").Body.String(), readOnlyTag) {
		t.Errorf("Root does not mark the read-only Mount")
	}
}

func TestNFCLinksResolveToEntriesStoredWithNFDNames(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	nfc := norm.NFC.String("café") // "café"
	nfd := norm.NFD.String(nfc)
	if nfc == nfd {
		t.Fatal("test names do not differ")
	}
	dir := filepath.Join(e.Dir, "docs", nfd)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nfd+".txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Skip only where the filesystem itself normalises names (then nothing is NFD).
	des, _ := os.ReadDir(filepath.Join(e.Dir, "docs"))
	if len(des) != 1 || des[0].Name() != nfd {
		t.Skip("filesystem normalises file names")
	}
	c := e.SignIn(testenv.DefaultUser)
	list := c.Get("/files/docs")
	if !strings.Contains(list.Body.String(), ">"+nfc+"<") {
		t.Fatalf("listing lacks the NFC name:\n%s", list.Body.String())
	}
	// The link the listing gives (NFC) must resolve to the NFD folder.
	folder := c.Get("/files/docs/" + url.PathEscape(nfc))
	if folder.Code != http.StatusOK {
		t.Fatalf("NFC link to NFD folder = %d, want 200", folder.Code)
	}
	if !strings.Contains(folder.Body.String(), ">"+nfc+".txt<") {
		t.Errorf("folder listing lacks its file:\n%s", folder.Body.String())
	}
	// Also when the NFD file name is below the NFD folder, and for a NFC link with NFD
	// percent-encoding ("e" + U+0301) sent by a browser.
	if rec := c.Get("/files/docs/" + url.PathEscape(nfd)); rec.Code != http.StatusOK {
		t.Errorf("NFD link = %d, want 200", rec.Code)
	}
}
