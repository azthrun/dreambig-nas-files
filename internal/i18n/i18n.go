// Package i18n looks up every UI string by key. Handlers and templates never contain
// literal user-facing text; they call T (templates: the "t" function). Ticket #6 turns
// the catalogs map into real per-language catalogs and picks the language per request;
// until then there is only English.
//
// Add new strings to the English catalog in this file, one key per string, named
// "<area>.<what>" (for example "signin.failed"). Values are fmt format strings.
package i18n

import "fmt"

// Default is the language used when none is chosen or a key is missing from a language.
const Default = "en"

var catalogs = map[string]map[string]string{
	Default: {
		"app.name":           "NAS Files",
		"nav.sign_out":       "Sign out",
		"signin.title":       "Sign in",
		"signin.name":        "Name",
		"signin.password":    "Password",
		"signin.submit":      "Sign in",
		"signin.failed":      "Sign-in failed. Check the name and password and try again.",
		"root.title":         "Files",
		"root.signed_in_as":  "Signed in as %s",
		"root.mounts":        "Folders",
		"root.no_mounts":     "No folders are available.",
		"root.read_only":     "read-only",
		"error.forbidden":    "Your request was refused. Reload the page and try again.",
		"error.title":        "Error",
		"error.not_found":    "Not found.",
		"error.unauthorized": "Sign in to continue.",
		"error.too_many":     "Too many attempts. Wait a while and try again.",
		"error.internal":     "Something went wrong.",
	},
}

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
