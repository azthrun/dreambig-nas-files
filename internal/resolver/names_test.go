package resolver_test

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

func TestNewNameRules(t *testing.T) {
	bad := []string{
		"", ".", "..", "a/b", `a\b`, "a:b", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b",
		"trailing.", "trailing ", "bad\x00nul", "tab\there", "\xff\xfe",
		"CON", "con", "Prn.txt", "AUX", "nul.tar.gz", "COM1", "lpt9.log",
		"@eaDir", "#recycle", ".DS_Store", ".nas-files-trash", ".nas-files-staging", ".nas-mcp-trash",
		strings.Repeat("a", 256),
	}
	for _, name := range bad {
		if _, err := resolver.ValidateName(name); !errors.Is(err, resolver.ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	if got := resolver.Status(resolver.ErrInvalidName); got != 400 {
		t.Errorf("Status(ErrInvalidName) = %d, want 400", got)
	}
	good := []string{"notes", "tax 2025", ".config", "a.b.c", "COM10", "console", "中文", strings.Repeat("a", 255)}
	for _, name := range good {
		if _, err := resolver.ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want ok", name, err)
		}
	}
}

func TestNewNameIsNormalisedToNFC(t *testing.T) {
	nfd := norm.NFD.String("é")
	got, err := resolver.ValidateName(nfd)
	if err != nil || got != norm.NFC.String(nfd) {
		t.Errorf("ValidateName(NFD) = %q, %v", got, err)
	}
}

func TestJoinNewAppliesTheRulesToAChild(t *testing.T) {
	parent, _ := resolver.Parse("docs/a")
	if p, err := parent.JoinNew("b"); err != nil || p.String() != "docs/a/b" {
		t.Errorf("JoinNew(b) = %v, %v", p, err)
	}
	if _, err := parent.JoinNew("b:c"); !errors.Is(err, resolver.ErrInvalidName) {
		t.Errorf("JoinNew(b:c) = %v", err)
	}
}
