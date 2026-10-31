package trash_test

import (
	"os"
	"path/filepath"
	"testing"
)

// A crash between setting a folder aside and removing it leaves the set-aside folder
// behind; the next purge finishes the job, whatever the retention says about its date.
func TestPurgeFinishesAFolderLeftSetAside(t *testing.T) {
	f := newFixture(t, 30)
	f.write(".nas-files-trash/2025-01-01.purging/old.txt", "old")
	f.write(".nas-files-trash/2026-01-10/recent.txt", "recent")

	f.trash.Purge()

	if _, err := os.Lstat(filepath.Join(f.dir, ".nas-files-trash", "2025-01-01.purging")); err == nil {
		t.Error("the set-aside folder survived")
	}
	if f.read(".nas-files-trash/2026-01-10/recent.txt") != "recent" {
		t.Error("a folder inside the retention was touched")
	}
}
