package trash_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/trash"
)

// The caller resolved the parent folder a moment ago. If a symlink took its place since,
// Place must notice right before the move instead of trashing whatever the link leads to.
func TestPlaceRefusesAParentThatIsNowASymlink(t *testing.T) {
	f := newFixture(t, 30)
	f.write("real/a.txt", "a")
	if err := os.Symlink("real", filepath.Join(f.dir, "swapped")); err != nil {
		t.Fatal(err)
	}

	_, err := f.trash.Place("docs", "swapped/a.txt")
	if !errors.Is(err, trash.ErrInvalid) {
		t.Fatalf("Place through a symlinked parent = %v, want ErrInvalid", err)
	}
	if f.read("real/a.txt") != "a" {
		t.Error("the file behind the link was moved")
	}
	if _, serr := os.Lstat(filepath.Join(f.dir, ".nas-files-trash")); serr == nil {
		t.Error("the Trash was touched")
	}
}

func TestPlaceRefusesAParentThatIsNoLongerAFolder(t *testing.T) {
	f := newFixture(t, 30)
	f.write("docs-file", "x")
	if _, err := f.trash.Place("docs", "docs-file/a.txt"); err == nil {
		t.Fatal("Place under a file succeeded")
	}
}
