package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
)

const langCookieLifetime = 365 * 24 * time.Hour

// langOf is the language of this request: saved cookie, then Accept-Language, then English.
func langOf(r *http.Request) string {
	saved := ""
	if c, err := r.Cookie(i18n.CookieName); err == nil {
		saved = c.Value
	}
	return i18n.Resolve(saved, r.Header.Get("Accept-Language"))
}

// switchLanguage handles GET /lang/<code>?next=<address>: it remembers the language in a
// cookie and redirects back. It is public and a plain GET link on purpose: the switch
// must work with JavaScript off and on the sign-in page, which has no session and no
// CSRF token. It is not a write needing CSRF: it only stores a display preference in the
// User's own browser and touches no files or session. The cost of a forged request is
// a changed UI language, which the cookie's SameSite=Lax and the one-click switch undo.
func (s *server) switchLanguage(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !i18n.Supported(code) {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     i18n.CookieName,
		Value:    code,
		Path:     "/",
		MaxAge:   int(langCookieLifetime.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // not Secure: plain HTTP on the LAN (ADR-0002)
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
}

// safeNext allows only a same-site address starting with a single "/"; anything else
// goes to the home page, so the switch cannot be used as an open redirect.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	return next
}

type langLink struct {
	Code    string
	Href    string
	Current bool
}

// langLinks builds the English / 中文 switch for the page at next.
func langLinks(current, next string) []langLink {
	var out []langLink
	for _, code := range i18n.Languages() {
		out = append(out, langLink{
			Code:    code,
			Href:    "/lang/" + code + "?next=" + url.QueryEscape(next),
			Current: code == current,
		})
	}
	return out
}
