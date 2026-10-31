package server_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func TestADeleteThatFailsWhileMovingIsAuditedOnce(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/a.txt", "a")
	c := e.SignIn(testenv.DefaultUser)

	// Hold the process-wide write lock so the request passes its checks, then waits at the
	// move; the file vanishes meanwhile, as if somebody else removed it.
	done := make(chan int, 1)
	_ = e.Trash.Locked(func(func(mount, rel string) (string, error)) error {
		go func() { done <- c.Post(folderURL("docs"), doDelete("a.txt")).Code }()
		time.Sleep(200 * time.Millisecond)
		if err := os.Remove(filepath.Join(e.Dir, "docs", "a.txt")); err != nil {
			t.Error(err)
		}
		return nil
	})
	if code := <-done; code != http.StatusNotFound {
		t.Fatalf("delete of a vanished item = %d, want 404", code)
	}

	var lines []map[string]any
	for _, l := range auditLines(t, e) {
		if l["operation"] == "delete" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || lines[0]["path"] != "docs/a.txt" {
		t.Fatalf("want exactly one delete audit line for docs/a.txt, got %v", lines)
	}
}
