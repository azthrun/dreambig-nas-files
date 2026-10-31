package trash_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
)

// A purge of a big old Trash folder can take minutes. Deletes must not wait for it.
func TestADeleteDoesNotWaitForARunningPurge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	tr, err := trash.New(trash.Config{
		Mounts:        []mounts.Mount{{Name: "docs", Dir: dir}},
		Clock:         clock.NewFake(day),
		RetentionDays: 30,
		// The removal of a purged folder hangs until the test lets it go.
		RemoveAll: func(root *os.Root, rel string) error {
			close(entered)
			<-release
			return root.RemoveAll(rel)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	write := func(rel string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".nas-files-trash/2025-01-01/old.txt")
	write("a.txt")

	purged := make(chan struct{})
	go func() { tr.Purge(); close(purged) }()
	<-entered

	placed := make(chan error, 1)
	go func() { _, err := tr.Place("docs", "a.txt"); placed <- err }()
	select {
	case err := <-placed:
		if err != nil {
			t.Fatalf("Place during a purge: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Place waited for the purge to finish")
	}

	close(release)
	<-purged
	if _, err := os.Lstat(filepath.Join(dir, ".nas-files-trash", "2025-01-01")); err == nil {
		t.Error("the old Trash folder survived the purge")
	}
	if _, err := os.Lstat(filepath.Join(dir, ".nas-files-trash", "2026-01-15", "a.txt")); err != nil {
		t.Errorf("the deleted file is not in today's Trash: %v", err)
	}
}
