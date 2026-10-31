package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/upload"
	"github.com/azthrun/dreambig-nas-files/internal/web"
)

// routes is the one place routes are registered.
//
// Public routes (no session) are only /healthz, sign-in (GET and POST /login), /static
// (the CSS and scripts the sign-in page itself needs) and /lang/<code> (the language
// switch, which also works on the sign-in page). Static assets and the language switch
// hold no file data; CODING_STANDARDS.md names them. Every other route MUST be
// registered with s.protected, which puts the session and CSRF checks in front of it.
//
// File routes must not use ServeMux path cleaning (it redirects ".." instead of letting
// the Path resolver refuse it); register a subtree pattern and let the handler parse
// r.URL.EscapedPath().
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /lang/{code}", s.switchLanguage) // public: works on the sign-in page
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web.Static())))

	mux.Handle("GET /{$}", s.protected(s.root))
	mux.Handle("POST /logout", s.protected(s.logout))
	mux.Handle(upload.BasePath, s.protected(s.uploads)) // tus: every method, CSRF on writes
	return s.wrap(s.withFiles(s.withRefusals(mux)))
}

// withFiles sends everything under /files straight to its handlers, bypassing the
// ServeMux, whose path cleaning would redirect "..", "." and "//" instead of letting
// the Path resolver refuse them. Everything under /files is served by filesRoute,
// always behind s.protected.
func (s *server) withFiles(mux http.Handler) http.Handler {
	files := s.protected(s.filesRoute)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files" || strings.HasPrefix(r.URL.Path, "/files/") {
			files.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// withRefusals answers requests no route takes. ServeMux would send its own English
// "404 page not found" or "405 method not allowed"; here its handler only supplies the
// status and the Allow header, and the page is the translated one. This is the one place
// that sees such a request, whether signed in or not, so it audits it too. The address
// is not recorded: it is no Path, and anyone can send anything.
func (s *server) withRefusals(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		answer := &statusOnly{header: w.Header(), status: http.StatusNotFound}
		h.ServeHTTP(answer, r)
		key := "error.not_found"
		if answer.status == http.StatusMethodNotAllowed {
			key = "error.method"
		}
		ev := audit.Event{Operation: "route", Outcome: audit.Refused, Status: answer.status}
		if sess := s.currentSession(w, r); sess != nil {
			ev.User = sess.User
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess))
		}
		s.Audit.Record(ev)
		s.errorPage(w, r, answer.status, key)
	})
}

// statusOnly keeps a handler's status and headers and drops its body.
type statusOnly struct {
	header http.Header
	status int
}

func (a *statusOnly) Header() http.Header         { return a.header }
func (a *statusOnly) WriteHeader(status int)      { a.status = status }
func (a *statusOnly) Write(b []byte) (int, error) { return len(b), nil }

// filesRoute dispatches /files/<Path>. The Path is parsed from the escaped URL path.
func (s *server) filesRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.browse(w, r)
	case http.MethodPost:
		s.filesPost(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		s.refuseRoute(w, r, http.StatusMethodNotAllowed)
	}
}

// filesPost dispatches POST /files/<Path> by the form field op: mkdir, zip (the browse
// selection form's download button, names= checkboxes of the folder in the URL), delete
// (the confirm page) and delete-confirm (the move to the Trash); the last three share
// the selection form.
func (s *server) filesPost(w http.ResponseWriter, r *http.Request) {
	switch r.PostFormValue("op") {
	case "mkdir":
		s.mkdir(w, r)
	case "zip":
		p, err := pathFromRequest(r)
		if err != nil {
			s.refuse(w, r, "zip", sessionOf(r).User, p.String(), err, false)
			return
		}
		s.zipSelection(w, r, sessionOf(r).User, p)
	case "delete":
		s.confirmDelete(w, r)
	case "delete-confirm":
		s.deleteItems(w, r)
	default:
		s.refuseRoute(w, r, http.StatusBadRequest)
	}
}

// refuseRoute audits and answers a /files request that no handler takes: a method or a
// form operation that does not exist.
func (s *server) refuseRoute(w http.ResponseWriter, r *http.Request, status int) {
	s.Audit.Record(audit.Event{User: sessionOf(r).User, Operation: "files", Path: requestedPath(r), Outcome: audit.Refused, Status: status})
	s.errorPage(w, r, status, "error.method")
}

// wrap applies middleware common to every route: security headers. Nothing here needs
// a secure context (ADR-0002).
func (s *server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// Probe GETs /healthz on addr, reaching a wildcard listen address via loopback. It is
// what the -healthcheck flag runs.
func Probe(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse NAS_FILES_ADDR %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return fmt.Errorf("GET /healthz: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz answered %d", resp.StatusCode)
	}
	return nil
}

// healthz answers 200 without a session and reveals nothing about the configuration.
func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok\n"))
}
