package trash_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
)

var day = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t     *testing.T
	dir   string // the Mount folder "docs"
	clk   *clock.Fake
	trash *trash.Trash
}

func newFixture(t *testing.T, retentionDays int) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(day)
	tr, err := trash.New(trash.Config{
		Mounts:        []mounts.Mount{{Name: "docs", Dir: dir}},
		Clock:         clk,
		RetentionDays: retentionDays,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return &fixture{t: t, dir: dir, clk: clk, trash: tr}
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(f.dir, filepath.FromSlash(rel)))
	return err == nil
}

func TestPlaceMovesAFileIntoTheDatedTrashKeepingItsPath(t *testing.T) {
	f := newFixture(t, 30)
	f.write("tax/2025.pdf", "hello")

	got, err := f.trash.Place("docs", "tax/2025.pdf")
	if err != nil {
		t.Fatal(err)
	}
	want := ".nas-files-trash/2026-01-15/tax/2025.pdf"
	if got != want {
		t.Errorf("Place = %q, want %q", got, want)
	}
	if f.exists("tax/2025.pdf") {
		t.Error("the original is still in place")
	}
	if c := f.read(want); c != "hello" {
		t.Errorf("trashed content = %q", c)
	}
}

func TestPlaceNeverOverwritesATrashEntryAndNumbersTheName(t *testing.T) {
	f := newFixture(t, 30)
	f.write("a/report.txt", "one")
	if _, err := f.trash.Place("docs", "a/report.txt"); err != nil {
		t.Fatal(err)
	}
	f.write("a/report.txt", "two")
	second, err := f.trash.Place("docs", "a/report.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.write("a/report.txt", "three")
	third, err := f.trash.Place("docs", "a/report.txt")
	if err != nil {
		t.Fatal(err)
	}

	if want := ".nas-files-trash/2026-01-15/a/report (2).txt"; second != want {
		t.Errorf("second = %q, want %q", second, want)
	}
	if want := ".nas-files-trash/2026-01-15/a/report (3).txt"; third != want {
		t.Errorf("third = %q, want %q", third, want)
	}
	for rel, want := range map[string]string{
		".nas-files-trash/2026-01-15/a/report.txt":     "one",
		".nas-files-trash/2026-01-15/a/report (2).txt": "two",
		".nas-files-trash/2026-01-15/a/report (3).txt": "three",
	} {
		if got := f.read(rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

func TestPlaceMovesAWholeFolder(t *testing.T) {
	f := newFixture(t, 30)
	f.write("old/sub/x.txt", "x")
	f.write("old/y.txt", "y")

	got, err := f.trash.Place("docs", "old")
	if err != nil {
		t.Fatal(err)
	}
	if f.exists("old") {
		t.Error("folder still in place")
	}
	if f.read(got+"/sub/x.txt") != "x" || f.read(got+"/y.txt") != "y" {
		t.Error("folder contents did not move with it")
	}

	f.write("old/z.txt", "z")
	again, err := f.trash.Place("docs", "old")
	if err != nil {
		t.Fatal(err)
	}
	if again == got {
		t.Errorf("second folder landed on the first: %q", again)
	}
	if f.read(got+"/y.txt") != "y" {
		t.Error("first trashed folder was disturbed")
	}
}

func TestPlaceMovesASymlinkItselfNotItsTarget(t *testing.T) {
	f := newFixture(t, 30)
	f.write("real/target.txt", "keep me")
	if err := os.Symlink("real/target.txt", filepath.Join(f.dir, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := f.trash.Place("docs", "link")
	if err != nil {
		t.Fatal(err)
	}
	if f.exists("link") {
		t.Error("the link is still in place")
	}
	if f.read("real/target.txt") != "keep me" {
		t.Error("the target was touched")
	}
	fi, err := os.Lstat(filepath.Join(f.dir, filepath.FromSlash(got)))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("trashed entry is not the symlink: %v %v", fi, err)
	}
}

// purgeFixture is a Trash that writes its audit lines to the returned buffer.
func purgeFixture(t *testing.T, retentionDays int) (*fixture, *bytes.Buffer) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(day)
	var buf bytes.Buffer
	tr, err := trash.New(trash.Config{
		Mounts:        []mounts.Mount{{Name: "docs", Dir: dir}},
		Clock:         clk,
		RetentionDays: retentionDays,
		Audit:         audit.New(&buf, clk),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return &fixture{t: t, dir: dir, clk: clk, trash: tr}, &buf
}

func TestPurgeRemovesDateFoldersOlderThanTheRetentionByTheFakeClock(t *testing.T) {
	f, buf := purgeFixture(t, 30)
	f.write(".nas-files-trash/2025-12-14/gone.txt", "1") // 32 days old
	f.write(".nas-files-trash/2025-12-15/gone.txt", "2") // 31 days old
	f.write(".nas-files-trash/2025-12-16/kept.txt", "3") // exactly 30 days old
	f.write(".nas-files-trash/2026-01-15/kept.txt", "4") // today
	f.write(".nas-files-trash/not-a-date/kept.txt", "5")

	f.trash.Purge()

	for _, rel := range []string{"2025-12-14", "2025-12-15"} {
		if f.exists(".nas-files-trash/" + rel) {
			t.Errorf("%s survived the purge", rel)
		}
	}
	for _, rel := range []string{"2025-12-16/kept.txt", "2026-01-15/kept.txt", "not-a-date/kept.txt"} {
		if !f.exists(".nas-files-trash/" + rel) {
			t.Errorf("%s was purged", rel)
		}
	}
	for _, want := range []string{
		`"operation":"trash_purge"`, `"path":"docs/.nas-files-trash/2025-12-14"`, `"path":"docs/.nas-files-trash/2025-12-15"`, `"outcome":"ok"`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("audit lacks %s:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "2025-12-16") {
		t.Errorf("audit names a folder that was kept:\n%s", buf.String())
	}

	f.clk.Advance(24 * time.Hour) // the 30-day-old folder is now 31 days old
	f.trash.Purge()
	if f.exists(".nas-files-trash/2025-12-16") {
		t.Error("2025-12-16 survived the next day's purge")
	}
}

func TestPurgeDoesNothingWhenRetentionIsZero(t *testing.T) {
	f, buf := purgeFixture(t, 0)
	f.write(".nas-files-trash/2020-01-01/old.txt", "old")
	f.trash.Purge()
	if !f.exists(".nas-files-trash/2020-01-01/old.txt") {
		t.Error("retention 0 means never, but the folder was purged")
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected audit: %s", buf.String())
	}
}

func TestPurgeSkipsReadOnlyMounts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nas-files-trash", "2020-01-01"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr, err := trash.New(trash.Config{
		Mounts:        []mounts.Mount{{Name: "archive", Dir: dir, ReadOnly: true}},
		Clock:         clock.NewFake(day),
		RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Purge()
	if _, err := os.Stat(filepath.Join(dir, ".nas-files-trash", "2020-01-01")); err != nil {
		t.Error("a read-only Mount's Trash was purged")
	}
}

func TestRunPurgesAtStartAndStopsWhenCancelled(t *testing.T) {
	f, _ := purgeFixture(t, 30)
	f.write(".nas-files-trash/2020-01-01/old.txt", "old")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.trash.Run(ctx, time.Hour); close(done) }()
	for tries := 0; f.exists(".nas-files-trash/2020-01-01"); tries++ {
		if tries == 1000 {
			t.Fatal("Run did not purge at start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestPlaceRefusesAReadOnlyMount(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := trash.New(trash.Config{
		Mounts: []mounts.Mount{{Name: "archive", Dir: dir, ReadOnly: true}},
		Clock:  clock.NewFake(day),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if _, err := tr.Place("archive", "a.txt"); !errors.Is(err, trash.ErrReadOnly) {
		t.Errorf("err = %v, want ErrReadOnly", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Error("file was touched")
	}
}

func TestPlaceRefusesWhatItMustNotTouch(t *testing.T) {
	f := newFixture(t, 30)
	f.write("a.txt", "a")
	f.write(".nas-files-trash/2026-01-01/old.txt", "old")
	for _, rel := range []string{".", "", ".nas-files-trash", ".nas-files-trash/2026-01-01/old.txt", "../x", "missing.txt"} {
		if _, err := f.trash.Place("docs", rel); err == nil {
			t.Errorf("Place(%q) succeeded", rel)
		}
	}
	if _, err := f.trash.Place("nope", "a.txt"); err == nil {
		t.Error("unknown Mount accepted")
	}
	if f.read("a.txt") != "a" || f.read(".nas-files-trash/2026-01-01/old.txt") != "old" {
		t.Error("something was disturbed")
	}
}
