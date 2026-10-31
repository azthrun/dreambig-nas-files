package mounts_test

import (
	"errors"
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

// Only a filesystem that cannot link stops startup. Any other trouble with the probe
// (a full disk, a denied create) says nothing about hard links, so the Mount still
// starts, with a warning that names the real cause.
func TestDiscoverWarnsInsteadOfFailingWhenTheProbeLinkHitsAnotherError(t *testing.T) {
	for name, errno := range map[string]syscall.Errno{"full": syscall.ENOSPC, "denied": syscall.EACCES} {
		t.Run(name, func(t *testing.T) {
			root := mountDir(t, "docs")
			link := func(old, new string) error {
				return &os.LinkError{Op: "link", Old: old, New: new, Err: errno}
			}
			ms, err := mounts.Discover(root, mounts.Options{Link: link})
			if err != nil {
				t.Fatalf("a Mount that merely failed the probe stopped startup: %v", err)
			}
			w := ms[0].Warning
			if w == "" || !strings.Contains(w, `"docs"`) || !strings.Contains(w, errno.Error()) {
				t.Errorf("warning does not name the Mount and the cause: %q", w)
			}
			if strings.Contains(strings.ToLower(w), "does not support hard links") {
				t.Errorf("warning blames hard links for another cause: %q", w)
			}
			if strings.Contains(w, root) {
				t.Errorf("warning names an absolute path: %q", w)
			}
			if ms[0].ReadOnly {
				t.Error("the Mount was demoted to read-only")
			}
		})
	}
}

func TestDiscoverWarnsWhenTheProbeCannotBeCreated(t *testing.T) {
	root := mountDir(t, "docs")
	dir := filepath.Join(root, "docs")
	// A file where the Staging Area should be: the probe cannot even be created.
	if err := os.WriteFile(filepath.Join(dir, mounts.StagingDir), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ms, err := mounts.Discover(root, mounts.Options{})
	if err != nil {
		t.Fatalf("startup failed: %v", err)
	}
	if ms[0].Warning == "" || strings.Contains(strings.ToLower(ms[0].Warning), "does not support hard links") {
		t.Errorf("warning = %q", ms[0].Warning)
	}
}

func TestDiscoverStillFailsOnEveryGenuineNoLinkError(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EPERM, syscall.ENOTSUP, syscall.EOPNOTSUPP} {
		root := mountDir(t, "docs")
		link := func(old, new string) error {
			return &os.LinkError{Op: "link", Old: old, New: new, Err: errno}
		}
		if _, err := mounts.Discover(root, mounts.Options{Link: link}); err == nil {
			t.Errorf("%v accepted", errno)
		}
	}
}

// A crash between creating the probe and removing it must not leave a file Users can
// see: the probe's names are Hidden Entries, and the next startup clears old ones.
func TestTheProbeIsHiddenAndALeftoverIsClearedAtStartup(t *testing.T) {
	root := mountDir(t, "docs")
	dir, err := filepath.EvalSymlinks(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	link := func(old, new string) error {
		for _, p := range []string{old, new} {
			rel, _ := filepath.Rel(dir, p)
			first := strings.Split(rel, string(filepath.Separator))[0]
			if !mounts.IsHiddenName(first) {
				t.Errorf("probe file %q is visible to Users", rel)
			}
			seen = append(seen, rel)
		}
		return errors.New("crash") // stand-in: stop without cleaning is simulated below
	}
	if _, err := mounts.Discover(root, mounts.Options{Link: link}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("link saw %v", seen)
	}
	// Simulate the leftovers of a crashed run, named the way the probe names its files.
	for _, rel := range seen {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mounts.Discover(root, mounts.Options{}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range seen {
		if _, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
			t.Errorf("leftover %q survived startup", rel)
		}
	}
}
