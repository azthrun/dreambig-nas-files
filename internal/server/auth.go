package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/i18n"
	"github.com/azthrun/dreambig-nas-files/internal/identity"
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
// header X-CSRF-Token). Every route except /healthz, sign-in and /static goes through
// it, registered in routes.go. Each refusal is audited.
func (s *server) protected(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := s.currentSession(w, r)
		if sess == nil {
			if safeMethod(r.Method) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			s.audit.Record(audit.Event{Operation: "session", Outcome: audit.Refused, Status: http.StatusUnauthorized})
			s.errorPage(w, r, http.StatusUnauthorized, "error.unauthorized")
			return
		}
		if !safeMethod(r.Method) && !validCSRF(r, sess) {
			s.audit.Record(audit.Event{User: sess.User, Operation: "csrf", Outcome: audit.Refused, Status: http.StatusForbidden})
			s.errorPage(w, r, http.StatusForbidden, "error.forbidden")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
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
	s.render(w, r, http.StatusOK, "login.html", page{Title: i18n.T(i18n.Default, "signin.title")})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	name, password := r.PostFormValue("name"), r.PostFormValue("password")
	userKey, ipKey := "user:"+name, "ip:"+clientIP(r)
	fail := func(status int, key string) {
		s.audit.Record(audit.Event{User: name, Operation: "signin", Outcome: audit.Refused, Status: status})
		s.render(w, r, status, "login.html", page{Title: i18n.T(i18n.Default, "signin.title"), Name: name, Error: i18n.T(i18n.Default, key)})
	}
	if s.limiter.blocked(userKey, ipKey) {
		fail(http.StatusTooManyRequests, "signin.failed")
		return
	}
	user, err := s.Auth.Authenticate(name, password)
	if err != nil {
		if !errors.Is(err, identity.ErrInvalidCredentials) {
			s.Log.Error("authenticate", "err", err)
		}
		s.limiter.fail(userKey, ipKey)
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
	var ms []mountView
	for _, m := range s.Mounts { // #5 narrows this to the Mounts the User has Access to
		ms = append(ms, mountView{Name: m.Name, Href: "/files/" + url.PathEscape(m.Name), ReadOnly: m.ReadOnly})
	}
	s.render(w, r, http.StatusOK, "root.html", page{Title: i18n.T(i18n.Default, "root.title"), Mounts: ms})
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
}

// render writes the named template with status. It fills the session-derived fields.
func (s *server) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	p.Lang = i18n.Default
	if sess := sessionOf(r); sess != nil {
		p.User, p.CSRF = sess.User, sess.CSRF
	}
	var buf strings.Builder
	if err := s.tmpl.ExecuteTemplate(&buf, name, p); err != nil {
		s.Log.Error("render", "template", name, "err", err)
		http.Error(w, i18n.T(i18n.Default, "error.internal"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(buf.String()))
}

// errorPage renders a plain error page for a refused request.
func (s *server) errorPage(w http.ResponseWriter, r *http.Request, status int, key string) {
	s.render(w, r, status, "error.html", page{Title: i18n.T(i18n.Default, "error.title"), Error: i18n.T(i18n.Default, key)})
}
