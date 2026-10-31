package resolver_test

import (
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

func TestNumberedPutsTheCountBeforeTheExtension(t *testing.T) {
	for _, c := range []struct {
		name  string
		n     int
		isDir bool
		want  string
	}{
		{"report.pdf", 0, false, "report.pdf"},
		{"report.pdf", 1, false, "report (1).pdf"},
		{"a.tar.gz", 2, false, "a.tar (2).gz"},
		{"notes", 3, false, "notes (3)"},
		{".config", 1, false, ".config (1)"},
		{"v1.2", 1, true, "v1.2 (1)"},
	} {
		if got := resolver.Numbered(c.name, c.n, c.isDir); got != c.want {
			t.Errorf("Numbered(%q, %d, %v) = %q, want %q", c.name, c.n, c.isDir, got, c.want)
		}
	}
}
