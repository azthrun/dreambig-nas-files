package server_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func TestFolderUploadCreatesMissingParentsOnlyAtCompletion(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/photos/2025/trip/a.txt", 5))
	if exists(e, "docs/photos") {
		t.Fatal("parents created before the Upload completed")
	}
	if rec := tusPatch(c, loc, 0, "hel"); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d", rec.Code)
	}
	if exists(e, "docs/photos") {
		t.Fatal("parents created by a partial Upload")
	}
	if rec := tusPatch(c, loc, 3, "lo"); rec.Code != http.StatusNoContent {
		t.Fatalf("last PATCH = %d: %s", rec.Code, rec.Body)
	}
	if got := e.ReadFile("docs/photos/2025/trip/a.txt"); got != "hello" {
		t.Fatalf("file = %q", got)
	}
	fi, err := os.Stat(filepath.Join(e.Dir, "docs/photos/2025"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o700 != 0o700 {
		t.Fatalf("parent = %v, %v", fi, err)
	}
}

func TestFolderUploadRefusesBadNamesInEverySegmentAndCreatesNothing(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	for _, p := range []string{
		"docs/new/a:b/x.txt",
		"docs/new/q?/x.txt",
		"docs/new/trail./x.txt",
		"docs/new/nul/x.txt",
		"docs/new/@eaDir/x.txt",
		"docs/new/#recycle/x.txt",
		"docs/new/.DS_Store/x.txt",
		"docs/new/.nas-files-trash/x.txt",
		"docs/new/../x.txt",
		"docs/new//x.txt",
		`docs/new/a\b/x.txt`,
	} {
		rec := tusCreate(c, p, 1)
		if rec.Code != 400 && rec.Code != 404 {
			t.Errorf("create %q = %d, want 400 or 404", p, rec.Code)
		}
	}
	if exists(e, "docs/new") {
		t.Error("a refused Upload created folders")
	}
}

func TestFolderUploadNeedsWriteAccessAndNeverTouchesAReadOnlyMount(t *testing.T) {
	e := upEnv(t)
	e.Symlink("docs/toarchive", "../archive")
	c := e.SignIn("alice")
	if rec := tusCreate(c, "archive/new/x.txt", 1); rec.Code != 403 {
		t.Errorf("read-only Mount = %d, want 403", rec.Code)
	}
	if rec := tusCreate(c, "docs/toarchive/new/x.txt", 1); rec.Code != 403 {
		t.Errorf("link into read-only Mount = %d, want 403", rec.Code)
	}
	bob := e.SignIn("bob")
	if rec := tusCreate(bob, "archive/new/x.txt", 1); rec.Code != 404 && rec.Code != 403 {
		t.Errorf("read Access = %d", rec.Code)
	}
	if exists(e, "archive/new") {
		t.Error("folder created in a read-only Mount")
	}
}

func TestFolderUploadNeverFollowsSymlinksOutOrThroughDanglingOnes(t *testing.T) {
	e := upEnv(t)
	outside := t.TempDir()
	e.Symlink("docs/out", outside)
	e.Symlink("docs/dangle", filepath.Join(outside, "nothing"))
	c := e.SignIn("alice")
	for _, p := range []string{"docs/out/new/x.txt", "docs/dangle/new/x.txt", "docs/dangle/x.txt"} {
		if rec := tusCreate(c, p, 1); rec.Code < 400 {
			t.Errorf("create %q = %d, want a refusal", p, rec.Code)
		}
	}
	if des, _ := os.ReadDir(outside); len(des) != 0 {
		t.Errorf("wrote outside every Mount: %v", des)
	}
}

func TestFolderUploadConflictsWithAFileStandingWhereAFolderIsNeeded(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/file.txt", "f")
	c := e.SignIn("alice")
	if rec := tusCreate(c, "docs/file.txt/x.txt", 1); rec.Code != http.StatusConflict {
		t.Errorf("create under a file = %d, want 409", rec.Code)
	}
	if rec := tusCreate(c, "docs/sub/file2/x/y.txt", 1); rec.Code != http.StatusCreated {
		t.Errorf("create = %d", rec.Code)
	}
}

// No choice can put a folder where a file stands, so the code must tell the page not to
// offer one.
func TestAFileWhereAFolderIsNeededIsNotAConflictTheUserCanSettle(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/file.txt", "x")
	c := e.SignIn("alice")
	rec := tusCreate(c, "docs/file.txt/new.txt", 3)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ERR_CONFLICT_PARENT") {
		t.Fatalf("create = %d %q", rec.Code, rec.Body)
	}

	// And when the file appears while the data is in flight.
	loc := uploadURL(t, tusCreate(c, "docs/later/new.txt", 3))
	e.WriteFile("docs/later", "x")
	rec = tusPatch(c, loc, 0, "abc")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ERR_CONFLICT_PARENT") {
		t.Fatalf("late = %d %q", rec.Code, rec.Body)
	}
}

func TestParentTurnedIntoAFileBeforeCompletionIsAConflictAndLeavesNoStagedData(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/late/x.txt", 2))
	e.WriteFile("docs/late", "in the way")
	rec := tusPatch(c, loc, 0, "hi")
	if rec.Code != http.StatusConflict {
		t.Fatalf("PATCH = %d, want 409", rec.Code)
	}
	if got := e.ReadFile("docs/late"); got != "in the way" {
		t.Errorf("file overwritten: %q", got)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}
}

func TestParentLinkedIntoAReadOnlyMountBeforeCompletionIsRefused(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/swap/deep/x.txt", 2))
	e.Symlink("docs/swap", "../archive")
	if rec := tusPatch(c, loc, 0, "hi"); rec.Code < 400 {
		t.Fatalf("PATCH = %d, want a refusal", rec.Code)
	}
	if exists(e, "archive/deep") {
		t.Error("created folders in a read-only Mount")
	}
}

func TestConcurrentUploadsCreatingTheSameParentsBothSucceed(t *testing.T) {
	for round := 0; round < 20; round++ {
		e := upEnv(t)
		const n = 8
		clients := make([]*testenv.Client, n) // a Client is not safe for concurrent use
		for i := range clients {
			clients[i] = e.SignIn("alice")
		}
		locs := make([]string, n)
		names := make([]string, n)
		for i := range locs {
			names[i] = "docs/shared/deep/" + string(rune('a'+i)) + ".txt"
			locs[i] = uploadURL(t, tusCreate(clients[i], names[i], 2))
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		codes := make([]int, n)
		for i := range locs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = tusPatch(clients[i], locs[i], 0, "ok").Code
			}()
		}
		close(start)
		wg.Wait()
		for i, code := range codes {
			if code != http.StatusNoContent {
				t.Fatalf("round %d upload %d = %d", round, i, code)
			}
			if got := e.ReadFile(names[i]); got != "ok" {
				t.Fatalf("%s = %q", names[i], got)
			}
		}
	}
}
