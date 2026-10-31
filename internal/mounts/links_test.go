package mounts_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
)

func mountDir(t *testing.T, name string) (root string) {
	t.Helper()
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// Uploads complete with a hard link; a filesystem that cannot make one would fail every
// Upload only after the data had travelled. The owner finds out at startup instead.
func TestDiscoverRefusesAWritableMountThatCannotHardLink(t *testing.T) {
	root := mountDir(t, "docs")
	cantLink := func(old, new string) error {
		return &os.LinkError{Op: "link", Old: old, New: new, Err: syscall.EPERM}
	}
	_, err := mounts.Discover(root, mounts.Options{Link: cantLink})
	if err == nil {
		t.Fatal("a Mount without hard links was accepted")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"docs"`) || !strings.Contains(strings.ToLower(msg), "hard link") {
		t.Errorf("error does not name the Mount and the problem: %v", err)
	}
	if strings.Contains(msg, root) {
		t.Errorf("error names an absolute path: %v", err)
	}
	if des, _ := os.ReadDir(filepath.Join(root, "docs")); len(des) != 0 {
		t.Errorf("the probe left %d entries behind", len(des))
	}
}

func TestDiscoverLeavesNothingBehindWhenHardLinksWork(t *testing.T) {
	root := mountDir(t, "docs")
	if _, err := mounts.Discover(root, mounts.Options{}); err != nil {
		t.Fatal(err)
	}
	if des, _ := os.ReadDir(filepath.Join(root, "docs")); len(des) != 0 {
		t.Errorf("the probe left %d entries behind", len(des))
	}
}

func TestDiscoverDoesNotProbeAReadOnlyMount(t *testing.T) {
	root := mountDir(t, "archive")
	dir := filepath.Join(root, "archive")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	probed := false
	ms, err := mounts.Discover(root, mounts.Options{Link: func(string, string) error { probed = true; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !ms[0].ReadOnly {
		t.Skip("the folder is writable anyway (running as root?)")
	}
	if probed {
		t.Error("a read-only Mount was probed")
	}
}
