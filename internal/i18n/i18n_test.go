package i18n_test

import (
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
)

func TestTLooksUpAndFormats(t *testing.T) {
	if got := i18n.T(i18n.Default, "signin.title"); got != "Sign in" {
		t.Errorf("got %q", got)
	}
	if got := i18n.T(i18n.Default, "root.signed_in_as", "alice"); got != "Signed in as alice" {
		t.Errorf("got %q", got)
	}
}

func TestUnknownKeyAndLanguageFallBack(t *testing.T) {
	if got := i18n.T(i18n.Default, "no.such.key"); got != "no.such.key" {
		t.Errorf("unknown key = %q", got)
	}
	if got := i18n.T("xx", "signin.title"); got != "Sign in" {
		t.Errorf("unknown language = %q, want the default language's text", got)
	}
}
