package download_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/download"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

func setup(t *testing.T) (string, *resolver.Resolver) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	ms, err := mounts.Discover(root, mounts.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := resolver.New(ms)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Close() })
	return filepath.Join(root, "docs"), res
}

func item(t *testing.T, res *resolver.Resolver, p, name string) download.Item {
	t.Helper()
	path, err := resolver.Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := res.Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	return download.Item{Name: name, Path: path, Loc: loc}
}

func run(t *testing.T, res *resolver.Resolver, allow func(resolver.Location) bool, items ...download.Item) (int, map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	omitted, err := download.Zip(context.Background(), &buf, res, allow, time.Unix(0, 0), "NOTE", items)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	return omitted, got
}

// An entry listed by ReadDir that is gone by the time it is resolved was lost to this
// download: it is listed and counted, unlike Hidden or no-Access entries.
func TestAnEntryThatVanishesBeforeItIsResolvedIsListedAndCounted(t *testing.T) {
	dir, res := setup(t)
	for _, n := range []string{"a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(dir, "f"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f", n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The first entry the walk lets through removes its siblings, whichever comes first.
	first := true
	allow := func(l resolver.Location) bool {
		if first {
			first = false
			for _, n := range []string{"a", "b", "c"} {
				if !strings.HasSuffix(l.Rel(), "/"+n) {
					_ = os.Remove(filepath.Join(dir, "f", n))
				}
			}
		}
		return true
	}
	omitted, got := run(t, res, allow, item(t, res, "docs/f", "f"))
	if omitted != 2 {
		t.Errorf("omitted = %d, want 2", omitted)
	}
	note := got["_incomplete.txt"]
	if strings.Count(note, "docs/f/") != 2 {
		t.Errorf("note = %q, want the two vanished Paths", note)
	}
}

func TestAFolderBeyondTheDepthLimitIsListedAndCounted(t *testing.T) {
	dir, res := setup(t)
	deep := dir
	for i := 0; i < 70; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Skip("path too long here")
	}
	omitted, got := run(t, res, func(resolver.Location) bool { return true }, item(t, res, "docs/d", "d"))
	if omitted == 0 || !strings.Contains(got["_incomplete.txt"], "docs/d/d/d") {
		t.Errorf("omitted = %d, note = %q", omitted, got["_incomplete.txt"])
	}
}

func TestTheNoteDoesNotReplaceASelectedFileOfTheSameName(t *testing.T) {
	dir, res := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "_incomplete.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "f"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make the folder a folder item that vanished before the walk.
	gone := item(t, res, "docs/f", "f")
	if err := os.RemoveAll(filepath.Join(dir, "f")); err != nil {
		t.Fatal(err)
	}
	omitted, got := run(t, res, func(resolver.Location) bool { return true }, item(t, res, "docs/_incomplete.txt", "_incomplete.txt"), gone)
	if omitted != 1 {
		t.Fatalf("omitted = %d", omitted)
	}
	if got["_incomplete.txt"] != "mine" {
		t.Errorf("the selected file was replaced or doubled: %q", got["_incomplete.txt"])
	}
	n := 0
	for name, body := range got {
		if name != "_incomplete.txt" && strings.HasPrefix(body, "NOTE") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want one note under another name, got %v", got)
	}
}
