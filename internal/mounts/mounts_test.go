package mounts_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
)

// layout creates the named folders (a trailing "!" makes a plain file instead) under a
// fresh data directory and returns it.
func layout(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		if f, ok := strings.CutSuffix(n, "!"); ok {
			if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.Mkdir(filepath.Join(root, n), 0o755); errors.Is(err, os.ErrExist) {
			t.Skip("case-insensitive filesystem cannot hold both names; covered on Linux CI")
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func discover(root string) ([]mounts.Mount, error) {
	// Temp folders share a filesystem, so the mount-point rule is off here.
	return mounts.Discover(root, mounts.Options{})
}

func TestDiscoverListsMountsSortedByName(t *testing.T) {
	got, err := discover(layout(t, "photos", "documents", "Music"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range got {
		names = append(names, m.Name)
	}
	if want := "Music documents photos"; strings.Join(names, " ") != want {
		t.Errorf("got %v, want %s", names, want)
	}
	if got[0].Path == "" || got[0].ReadOnly {
		t.Errorf("mount record incomplete: %+v", got[0])
	}
}

func TestDiscoverRefusals(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		want    string // substring of the error, which must name the offender
	}{
		{"no mounts", nil, "no Mounts"},
		{"file instead of folder", []string{"docs", "notes.txt!"}, "notes.txt"},
		{"dot name", []string{".git"}, ".git"},
		{"at-sign name", []string{"@eaDir"}, "@eaDir"},
		{"hash name", []string{"#recycle"}, "#recycle"},
		{"trash name", []string{".nas-files-trash"}, ".nas-files-trash"},
		{"mcp trash name", []string{".nas-mcp-trash"}, ".nas-mcp-trash"},
		{"staging name", []string{mounts.StagingDir}, mounts.StagingDir},
		{"case clash", []string{"Docs", "docs"}, "Docs"},
		{"unicode case clash", []string{"Café", "café"}, "Café"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := discover(layout(t, tc.entries...))
			if err == nil {
				t.Fatal("startup must fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestDiscoverRefusesSymlinkEntry(t *testing.T) {
	root := layout(t, "real")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(root); err == nil || !strings.Contains(err.Error(), "link") {
		t.Errorf("a symlink is not a folder, got %v", err)
	}
}

func TestDiscoverRefusesMissingRoot(t *testing.T) {
	if _, err := discover(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing data directory must fail startup")
	}
}

func TestDiscoverRequiresOwnMountPointByDefault(t *testing.T) {
	// A sub-folder of a temp dir lives on the same filesystem as its parent.
	_, err := mounts.Discover(layout(t, "docs"), mounts.Options{RequireMountPoints: true})
	if err == nil || !strings.Contains(err.Error(), "docs") || !strings.Contains(err.Error(), "mount point") {
		t.Errorf("want a mount point error naming docs, got %v", err)
	}
}

func TestDiscoverDetectsReadOnlyMount(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := layout(t, "archive", "inbox")
	if err := os.Chmod(filepath.Join(root, "archive"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "archive"), 0o755) })

	got, err := discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].ReadOnly || got[1].ReadOnly {
		t.Errorf("archive must be read-only and inbox writable: %+v", got)
	}
	// The folder is not mounted :ro, only unwritable by this user: the owner is told.
	if got[0].Warning == "" || got[1].Warning != "" {
		t.Errorf("only the unwritable-but-not-:ro Mount should warn: %+v", got)
	}
}

func TestDiscoverRefusesUnreadableMount(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := layout(t, "locked")
	if err := os.Chmod(filepath.Join(root, "locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o755) })
	if _, err := discover(root); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("want an error naming locked, got %v", err)
	}
}

func TestIsHiddenName(t *testing.T) {
	for _, n := range []string{"@eaDir", "@anything", "#recycle", "#snapshot", ".DS_Store", ".ds_store", ".nas-files-trash", ".NAS-MCP-TRASH", mounts.StagingDir} {
		if !mounts.IsHiddenName(n) {
			t.Errorf("%q must be hidden", n)
		}
	}
	for _, n := range []string{"docs", "report.pdf", "文档", "a@b"} {
		if mounts.IsHiddenName(n) {
			t.Errorf("%q must not be hidden", n)
		}
	}
}
