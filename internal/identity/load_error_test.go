package identity_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/identity"
)

func TestLoadErrorsDoNotNameThePath(t *testing.T) {
	dir := t.TempDir()
	_, err := identity.Load(filepath.Join(dir, "users"))
	if err == nil {
		t.Fatal("missing users file accepted")
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("error names the path: %v", err)
	}
	if !strings.Contains(err.Error(), "users file") {
		t.Errorf("error does not say which file it is about: %v", err)
	}
}
