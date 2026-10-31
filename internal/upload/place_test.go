package upload_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
	"github.com/azthrun/dreambig-nas-files/internal/upload"
)

// racyTrash is a Trash whose placements are followed by someone else creating a file at
// the same Path (another process on the NAS, outside our write lock).
type racyTrash struct {
	*trash.Trash
	appear func()
}

func (r racyTrash) Locked(fn func(place func(mount, rel string) (string, error)) error) error {
	return r.Trash.Locked(func(place func(mount, rel string) (string, error)) error {
		return fn(func(mount, rel string) (string, error) {
			dest, err := place(mount, rel)
			r.appear()
			return dest, err
		})
	})
}

type fixture struct {
	dir  string
	loc  resolver.Location
	tr   *trash.Trash
	stg  string // staged name
	root string
}

func newFixture(t *testing.T, stage bool) fixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir()) // Mount paths are real
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.pdf"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, mounts.StagingDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if stage {
		if err := os.WriteFile(filepath.Join(dir, mounts.StagingDir, "abc"), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := []mounts.Mount{{Name: "docs", Path: dir}}
	res, err := resolver.New(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Close() })
	tr, err := trash.New(trash.Config{Mounts: m, Clock: clock.NewFake(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	p, err := resolver.Parse("docs/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	loc, err := res.Resolve(p)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{dir: dir, loc: loc, tr: tr, stg: mounts.StagingDir + "/abc"}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReplaceNeverLosesTheOldFileWhenTheNewOneCannotBeLinked(t *testing.T) {
	f := newFixture(t, false) // the staged data is gone, so the link fails after the old file was trashed
	p := upload.ChoicePlacer{Trash: f.tr}

	err := p.Place(context.Background(), upload.Placement{User: "alice", Dest: f.loc, Staged: f.stg, Choice: upload.ChoiceReplace})

	if err == nil {
		t.Fatal("Place succeeded without staged data")
	}
	if got := read(t, filepath.Join(f.dir, "report.pdf")); got != "old" {
		t.Fatalf("the old file was lost or changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".nas-files-trash", "2026-01-15", "report.pdf")); err == nil {
		t.Error("the restored file is also left in the Trash")
	}
}

func TestReplaceThatMeetsALateFileKeepsTheOldOneInTheTrashAndNeverOverwrites(t *testing.T) {
	f := newFixture(t, true)
	appear := func() {
		if err := os.WriteFile(filepath.Join(f.dir, "report.pdf"), []byte("late"), 0o644); err != nil {
			t.Error(err)
		}
	}
	p := upload.ChoicePlacer{Trash: racyTrash{Trash: f.tr, appear: appear}}

	err := p.Place(context.Background(), upload.Placement{User: "alice", Dest: f.loc, Staged: f.stg, Choice: upload.ChoiceReplace})

	if !errors.Is(err, upload.ErrConflict) {
		t.Fatalf("err = %v, want a Conflict", err)
	}
	if got := read(t, filepath.Join(f.dir, "report.pdf")); got != "late" {
		t.Fatalf("the late file was overwritten: %q", got)
	}
	if got := read(t, filepath.Join(f.dir, ".nas-files-trash", "2026-01-15", "report.pdf")); got != "old" {
		t.Fatalf("the old file is not in the Trash: %q", got)
	}
	if got := read(t, filepath.Join(f.dir, f.stg)); got != "new" {
		t.Error("the staged data was not kept for the User's choice")
	}
}
