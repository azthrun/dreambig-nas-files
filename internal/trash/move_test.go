package trash_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
)

func newMover(t *testing.T, readOnly bool) (*trash.Trash, string) {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := trash.New(trash.Config{Mounts: []mounts.Mount{{Name: "docs", Dir: dir, ReadOnly: readOnly}}, Clock: clock.Real{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr, dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMoveNeverReplacesAFileOrAFolder(t *testing.T) {
	tr, dir := newMover(t, false)
	write(t, dir, "a.txt", "a")
	write(t, dir, "b.txt", "b")
	write(t, dir, "f/x", "x")
	write(t, dir, "g/y", "y")

	for _, c := range [][2]string{{"a.txt", "b.txt"}, {"a.txt", "g"}, {"f", "b.txt"}, {"f", "g"}} {
		if err := tr.Move("docs", c[0], c[1]); !errors.Is(err, trash.ErrTaken) {
			t.Errorf("%v: err = %v", c, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(b) != "b" {
		t.Error("the file was replaced")
	}
}

func TestMoveRefusesWhatItMustNotTouch(t *testing.T) {
	tr, dir := newMover(t, false)
	write(t, dir, "a.txt", "a")
	write(t, dir, "f/x", "x")
	write(t, dir, "real/y", "y")
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		from, to string
		want     error
	}{
		{"", "b", trash.ErrInvalid},
		{".", "b", trash.ErrInvalid},
		{"../a.txt", "b", trash.ErrInvalid},
		{"a.txt", ".nas-files-trash/a.txt", trash.ErrInvalid},
		{"a.txt", "a.txt", trash.ErrInvalid},
		{"f", "f/inner", trash.ErrInvalid},
		{"missing", "b", trash.ErrNotFound},
		{"a.txt", "missing/b", trash.ErrNotFound},
		{"a.txt", "link/b", trash.ErrInvalid}, // a folder that became a link
		{"link/y", "b", trash.ErrInvalid},
	}
	for _, c := range cases {
		if err := tr.Move("docs", c.from, c.to); !errors.Is(err, c.want) {
			t.Errorf("%s -> %s: err = %v, want %v", c.from, c.to, err, c.want)
		}
	}
	if err := tr.Move("nope", "a.txt", "b"); !errors.Is(err, trash.ErrNoMount) {
		t.Errorf("unknown Mount: %v", err)
	}
}

func TestMoveRefusesAReadOnlyMount(t *testing.T) {
	tr, dir := newMover(t, true)
	write(t, dir, "a.txt", "a")
	if err := tr.Move("docs", "a.txt", "b.txt"); !errors.Is(err, trash.ErrReadOnly) {
		t.Errorf("err = %v", err)
	}
}
