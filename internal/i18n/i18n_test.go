package i18n_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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

// Every language must carry every key, checked in both directions, so a later ticket
// that adds a string to one catalog only fails here.
func TestEveryLanguageHasExactlyTheSameKeys(t *testing.T) {
	langs := i18n.Languages()
	if len(langs) < 2 {
		t.Fatalf("languages = %v, want at least en and zh-Hans", langs)
	}
	for _, a := range langs {
		for _, b := range langs {
			if a == b {
				continue
			}
			for _, key := range i18n.Keys(a) {
				if !slices.Contains(i18n.Keys(b), key) {
					t.Errorf("key %q is in %s but missing from %s", key, a, b)
				}
			}
		}
	}
}

func TestEveryLanguageHasTextAndMatchingFormatVerbs(t *testing.T) {
	verbs := regexp.MustCompile(`%[a-z]`)
	for _, lang := range i18n.Languages() {
		for _, key := range i18n.Keys(lang) {
			text := i18n.Text(lang, key)
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s: key %q is empty", lang, key)
			}
			if got, want := verbs.FindAllString(text, -1), verbs.FindAllString(i18n.Text(i18n.Default, key), -1); !slices.Equal(got, want) {
				t.Errorf("%s: key %q has verbs %v, English has %v", lang, key, got, want)
			}
		}
	}
}

func TestChineseTextIsReallyTranslated(t *testing.T) {
	if got := i18n.T("zh-Hans", "signin.submit"); got != "登录" {
		t.Errorf("got %q", got)
	}
}

func TestResolveOrderIsCookieThenAcceptLanguageThenEnglish(t *testing.T) {
	cases := []struct{ name, cookie, accept, want string }{
		{"nothing", "", "", "en"},
		{"accept chinese", "", "zh-CN,zh;q=0.9,en;q=0.8", "zh-Hans"},
		{"accept english first", "", "en-US,en;q=0.9,zh;q=0.8", "en"},
		{"q-values decide", "", "en;q=0.3, zh-CN;q=0.9", "zh-Hans"},
		{"traditional falls to simplified", "", "zh-TW", "zh-Hans"},
		{"unsupported then supported", "", "fr-FR,fr;q=0.9,zh;q=0.5", "zh-Hans"},
		{"unsupported only", "", "fr, de", "en"},
		{"q=0 refuses a language", "", "zh;q=0, en;q=0.1", "en"},
		{"cookie beats accept", "en", "zh-CN", "en"},
		{"cookie chinese beats accept english", "zh-Hans", "en", "zh-Hans"},
		{"bad cookie ignored", "klingon", "zh-CN", "zh-Hans"},
		{"garbage accept", "", ";;;q=,,x", "en"},
	}
	for _, c := range cases {
		if got := i18n.Resolve(c.cookie, c.accept); got != c.want {
			t.Errorf("%s: Resolve(%q, %q) = %q, want %q", c.name, c.cookie, c.accept, got, c.want)
		}
	}
}

func TestSizesAndDatesFollowTheLanguage(t *testing.T) {
	when := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	cases := []struct {
		lang, size0, size2k, date string
	}{
		{"en", "5 B", "2.0 KB", "2025-03-04 05:06"},
		{"zh-Hans", "5 字节", "2.0 KB", "2025年3月4日 05:06"},
	}
	for _, c := range cases {
		if got := i18n.FormatSize(c.lang, 5); got != c.size0 {
			t.Errorf("%s size 5 = %q, want %q", c.lang, got, c.size0)
		}
		if got := i18n.FormatSize(c.lang, 2048); got != c.size2k {
			t.Errorf("%s size 2048 = %q, want %q", c.lang, got, c.size2k)
		}
		if got := i18n.FormatTime(c.lang, when); got != c.date {
			t.Errorf("%s date = %q, want %q", c.lang, got, c.date)
		}
	}
}
