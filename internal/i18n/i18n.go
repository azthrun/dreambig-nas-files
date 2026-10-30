// Package i18n looks up every UI string by key and formats dates and sizes per language.
// Handlers and templates never contain literal user-facing text; they call T (templates:
// the "t" function).
//
// Languages are "en" and "zh-Hans". Every key must exist in every catalog: a test fails
// when one is missing in either direction, so a ticket that adds a string adds it to
// catalog_en.go AND catalog_zh.go, one key per string, named "<area>.<what>" (for
// example "signin.failed"). Values are fmt format strings with the same verbs in every
// language.
package i18n

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Default is the language used when none is chosen or a key is missing from a language.
const Default = "en"

// Chinese is the Simplified Chinese language tag.
const Chinese = "zh-Hans"

// CookieName is the cookie that remembers the manual language choice.
const CookieName = "nas_files_lang"

var catalogs = map[string]map[string]string{
	Default: english,
	Chinese: chinese,
}

// Languages lists the supported languages, English first.
func Languages() []string {
	out := []string{Default}
	var rest []string
	for l := range catalogs {
		if l != Default {
			rest = append(rest, l)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// Supported reports whether lang is a language with a catalog.
func Supported(lang string) bool { _, ok := catalogs[lang]; return ok }

// Keys lists the keys of lang's catalog, sorted. Used by the catalog tests.
func Keys(lang string) []string {
	var out []string
	for k := range catalogs[lang] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Text returns the raw catalog text for key in exactly lang, empty if it has none.
func Text(lang, key string) string { return catalogs[lang][key] }

// T returns the text for key in lang, formatted with args. A key missing from lang falls
// back to the Default language; a key missing everywhere comes back as the key itself,
// so a gap is visible rather than blank.
func T(lang, key string, args ...any) string {
	s, ok := catalogs[lang][key]
	if !ok {
		s, ok = catalogs[Default][key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Resolve picks the language for a request: a saved cookie value first, then the
// Accept-Language header (by q-value; any Chinese variant maps to Simplified), then
// English.
func Resolve(cookie, acceptLanguage string) string {
	if Supported(cookie) {
		return cookie
	}
	best, bestQ := "", 0.0
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				continue
			}
			q = f
		}
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		var lang string
		switch primary {
		case "en":
			lang = Default
		case "zh":
			lang = Chinese
		default:
			continue
		}
		if q > bestQ { // ties keep the earlier (more preferred) entry
			best, bestQ = lang, q
		}
	}
	if best == "" {
		return Default
	}
	return best
}

// FormatSize writes a byte count with 1024-based units, in lang's wording.
func FormatSize(lang string, n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d %s", n, T(lang, "unit.bytes"))
	}
	f, suffix := float64(n), ""
	for _, suffix = range []string{"KB", "MB", "GB", "TB"} {
		f /= unit
		if f < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", f, suffix)
}

// FormatTime writes t (in UTC) as date and minute in lang's usual order; the layout is
// the catalog entry "format.datetime".
func FormatTime(lang string, t time.Time) string {
	return t.UTC().Format(T(lang, "format.datetime"))
}
