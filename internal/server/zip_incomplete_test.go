package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipAuditLine(t *testing.T, audit string) map[string]any {
	t.Helper()
	for _, l := range strings.Split(strings.TrimSpace(audit), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["operation"] == "zip" {
			return m
		}
	}
	t.Fatalf("no zip audit line in %s", audit)
	return nil
}

func TestZipListsWhatItCouldNotReadAndIsAuditedPartial(t *testing.T) {
	e := zipEnv(t)
	locked := filepath.Join(e.Dir, "docs/proj/locked.txt")
	e.WriteFile("docs/proj/locked.txt", "nope")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o644) })
	if f, err := os.Open(locked); err == nil { // running as root: the file stays readable
		f.Close()
		t.Skip("cannot make a file unreadable here")
	}
	e.WriteFile("docs/proj/@eaDir/thumb.jpg", "x") // Hidden: stays silently out

	got, _ := readZip(t, e.SignIn("bob").Get("/files/docs/proj?zip=1"))
	note, ok := got["_incomplete.txt"]
	if !ok {
		t.Fatalf("no _incomplete.txt in %v", names(got))
	}
	if !strings.Contains(note, "docs/proj/locked.txt") {
		t.Errorf("note does not list the Path left out: %q", note)
	}
	if strings.Contains(note, "eaDir") || strings.Contains(note, "a.txt") {
		t.Errorf("note lists more than the failures: %q", note)
	}
	if _, ok := got["proj/locked.txt"]; ok {
		t.Error("the unreadable file is in the zip")
	}
	if got["proj/a.txt"] != "aaa" {
		t.Error("the rest of the zip was not streamed")
	}
	ev := zipAuditLine(t, e.Audit.String())
	if ev["outcome"] != "partial" || ev["path"] != "docs/proj" {
		t.Errorf("audit = %v", ev)
	}
}

func TestACompleteZipHasNoNoteAndIsAuditedOK(t *testing.T) {
	e := zipEnv(t)
	got, _ := readZip(t, e.SignIn("bob").Get("/files/docs/proj?zip=1"))
	if _, ok := got["_incomplete.txt"]; ok {
		t.Error("a complete zip carries a note")
	}
	if ev := zipAuditLine(t, e.Audit.String()); ev["outcome"] != "ok" {
		t.Errorf("audit = %v", ev)
	}
}
