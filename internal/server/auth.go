package server

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"github.com/azthrun/dreambig-nas-files/internal/access"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/i18n"
	"github.com/azthrun/dreambig-nas-files/internal/identity"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/upload"
)

const (
	sessionCookie = "nas_files_session"
	csrfHeader    = "X-CSRF-Token"
	csrfField     = "csrf_token"
)

type ctxKey struct{}

// sessionOf returns the signed-in session of a request that went through s.protected.
// Handlers read the User from it: sessionOf(r).User.
func sessionOf(r *http.Request) *session {
	s, _ := r.Context().Value(ctxKey{}).(*session)
	return s
}

// protected wraps h so it only runs for a signed-in User, and, for any method other
// than GET, HEAD and OPTIONS, only with a valid CSRF token (form field csrf_token or
// header X-CSRF-Token). Every route except /healthz, sign-in, /static and /lang goes
// through it, registered in routes.go. Each refusal is audited.
func (s *server) protected(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := s.currentSession(w, r)
		if sess == nil {
			if safeMethod(r.Method) {
				s.audit.Record(audit.Event{Operation: "session", Path: requestedPath(r), Outcome: audit.Refused, Status: http.StatusSeeOther})
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			s.audit.Record(audit.Event{Operation: "session", Path: requestedPath(r), Outcome: audit.Refused, Status: http.StatusUnauthorized})
			s.errorPage(w, r, http.StatusUnauthorized, "error.unauthorized")
			return
		}
		if !safeMethod(r.Method) && !validCSRF(r, sess) {
			s.audit.Record(audit.Event{User: sess.User, Operation: "csrf", Path: requestedPath(r), Outcome: audit.Refused, Status: http.StatusForbidden})
			s.errorPage(w, r, http.StatusForbidden, "error.forbidden")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

// requestedPath is the Path a request names, for the audit line of a request that was
// refused before any handler ran: /files requests carry it in the URL, an Upload
// creation in its tus metadata, a skip report in its form field. Empty where the request
// names none (an Upload id alone) and for a string that is not a valid Path. It is
// only for the audit line; nothing of it is ever sent back.
func requestedPath(r *http.Request) string {
	var raw string
	switch {
	case strings.HasPrefix(r.URL.Path, filesPrefix+"/"):
		p, err := pathFromRequest(r)
		if err != nil {
			return ""
		}
		return p.String()
	case r.Method == http.MethodPost && (r.URL.Path == upload.BasePath || r.URL.Path == strings.TrimSuffix(upload.BasePath, "/")):
		raw = tusMetadata(r.Header.Get("Upload-Metadata"), "path")
	case r.Method == http.MethodPost && r.URL.Path == upload.BasePath+"skip":
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
			raw = r.PostFormValue("path")
		}
	}
	p, err := resolver.Parse(raw)
	if raw == "" || err != nil {
		return ""
	}
	return p.String()
}

// tusMetadata reads one value of a tus Upload-Metadata header ("key base64,key base64").
func tusMetadata(header, key string) string {
	for _, pair := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(pair), " ")
		if k != key {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
	return ""
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func validCSRF(r *http.Request, sess *session) bool {
	got := r.Header.Get(csrfHeader)
	if got == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		got = r.PostFormValue(csrfField)
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) == 1
}

// currentSession finds the session from the cookie and renews it (sliding expiry).
func (s *server) currentSession(w http.ResponseWriter, r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	sess := s.sessions.touch(c.Value)
	if sess == nil {
		return nil
	}
	setSessionCookie(w, c.Value, int(sessionLifetime.Seconds()))
	return sess
}

func setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, // not Secure: plain HTTP (ADR-0002)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) loginForm(w http.ResponseWriter, r *http.Request) {
	if s.currentSession(w, r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login.html", page{Title: i18n.T(langOf(r), "signin.title")})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	name, password := r.PostFormValue("name"), r.PostFormValue("password")
	keys := []string{userKey(name), ipKey(clientIP(r))}
	fail := func(status int, key string) {
		s.audit.Record(audit.Event{User: name, Operation: "signin", Outcome: audit.Refused, Status: status})
		s.render(w, r, status, "login.html", page{Title: i18n.T(langOf(r), "signin.title"), Name: name, Error: i18n.T(langOf(r), key)})
	}
	if s.limiter.blocked(keys...) {
		fail(http.StatusTooManyRequests, "signin.failed")
		return
	}
	user, err := s.Auth.Authenticate(name, password)
	if err != nil {
		if !errors.Is(err, identity.ErrInvalidCredentials) {
			s.Log.Error("authenticate", "err", err)
		}
		s.limiter.fail(keys...)
		fail(http.StatusUnauthorized, "signin.failed")
		return
	}
	token, _ := s.sessions.create(user.Name)
	setSessionCookie(w, token, int(sessionLifetime.Seconds()))
	s.audit.Record(audit.Event{User: user.Name, Operation: "signin", Outcome: audit.OK, Status: http.StatusSeeOther})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.delete(c.Value)
	}
	setSessionCookie(w, "", -1)
	s.audit.Record(audit.Event{User: sessionOf(r).User, Operation: "signout", Outcome: audit.OK, Status: http.StatusSeeOther})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type mountView struct {
	Name     string
	Href     string
	ReadOnly bool
}

func (s *server) root(w http.ResponseWriter, r *http.Request) {
	user := sessionOf(r).User
	var ms []mountView
	for _, m := range s.Mounts {
		lvl := s.Access.Level(user, m)
		if lvl < access.Read { // a Mount without Access does not exist for this User
			continue
		}
		ms = append(ms, mountView{Name: m.Name, Href: "/files/" + url.PathEscape(m.Name), ReadOnly: lvl < access.Write})
	}
	s.render(w, r, http.StatusOK, "root.html", page{Title: i18n.T(langOf(r), "root.title"), Mounts: ms})
}

// page is the data every template receives. render fills Lang, User and CSRF.
type page struct {
	Lang   string
	Title  string
	User   string
	CSRF   string
	Name   string
	Error  string
	Mounts []mountView
	Browse *browseView
	// Confirm is the delete confirmation page's data.
	Confirm *confirmView

	Next      string // this page's own address, for the language switch to return to
	LangLinks []langLink
}

// render writes the named template with status. It fills the session-derived fields.
func (s *server) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	p.Lang = langOf(r)
	if p.Next == "" {
		p.Next = r.URL.RequestURI()
	}
	p.LangLinks = langLinks(p.Lang, p.Next)
	if sess := sessionOf(r); sess != nil {
		p.User, p.CSRF = sess.User, sess.CSRF
	}
	var buf strings.Builder
	if err := s.tmpl.ExecuteTemplate(&buf, name, p); err != nil {
		s.Log.Error("render", "template", name, "err", err)
		http.Error(w, i18n.T(langOf(r), "error.internal"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(buf.String()))
}

// errorPage renders a plain error page for a refused request.
func (s *server) errorPage(w http.ResponseWriter, r *http.Request, status int, key string) {
	s.render(w, r, status, "error.html", page{Title: i18n.T(langOf(r), "error.title"), Error: i18n.T(langOf(r), key),
		Next: "/"}) // never echo the refused address into the page, even in the switch links
}
