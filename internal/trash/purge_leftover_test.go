package trash_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
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

// Retention 0 means "keep forever", but a folder already set aside is half-deleted and
// nobody can reach it: finishing it is not a purge of something kept.
func TestPurgeFinishesALeftoverEvenWhenRetentionIsOff(t *testing.T) {
	f := newFixture(t, 0)
	f.write(".nas-files-trash/2025-01-01.purging/old.txt", "old")
	f.write(".nas-files-trash/2025-01-02/kept.txt", "kept")

	f.trash.Purge()

	if f.exists(".nas-files-trash/2025-01-01.purging") {
		t.Error("the set-aside folder was left behind")
	}
	if !f.exists(".nas-files-trash/2025-01-02/kept.txt") {
		t.Error("with retention off, a dated folder was purged")
	}
}

func TestAFailedPurgeKeepsItsCauseWithoutTheOSPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.MkdirAll(filepath.Join(dir, ".nas-files-trash", "2020-01-01"), 0o755); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	tr, err := trash.New(trash.Config{
		Mounts: []mounts.Mount{{Name: "docs", Path: dir}}, Clock: clock.NewFake(day), RetentionDays: 30,
		Log: slog.New(slog.NewJSONHandler(&log, nil)),
		RemoveAll: func(*os.Root, string) error {
			return &os.PathError{Op: "unlinkat", Path: dir, Err: syscall.EIO}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	tr.Purge()
	if !strings.Contains(log.String(), syscall.EIO.Error()) {
		t.Errorf("the cause is lost: %q", log.String())
	}
	if strings.Contains(log.String(), dir) {
		t.Errorf("the log echoes the OS path: %q", log.String())
	}
}
