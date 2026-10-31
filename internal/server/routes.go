package server

import (
	"net/http"
	"strings"

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
	return s.wrap(s.withFiles(mux))
}

// withFiles sends everything under /files straight to its handlers, bypassing the
// ServeMux, whose path cleaning would redirect "..", "." and "//" instead of letting
// the Path resolver refuse them. Later tickets add their /files/... methods in
// filesRoute, always behind s.protected.
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

// filesRoute dispatches /files/<Path>. The Path is parsed from the escaped URL path.
func (s *server) filesRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.browse(w, r)
	case http.MethodPost:
		s.filesPost(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		s.errorPage(w, r, http.StatusMethodNotAllowed, "error.method")
	}
}

// filesPost dispatches POST /files/<Path> by the form field op. Later tickets add their
// plain-form operations here. op=zip is the browse selection form's download button
// (names= checkboxes of the folder in the URL); op=delete (confirm page) and
// op=delete-confirm (the move to the Trash) share that form.
func (s *server) filesPost(w http.ResponseWriter, r *http.Request) {
	switch r.PostFormValue("op") {
	case "mkdir":
		s.mkdir(w, r)
	case "zip":
		p, err := pathFromRequest(r)
		if err != nil {
			s.refuseBrowse(w, r, sessionOf(r).User, p, err)
			return
		}
		s.zipSelection(w, r, sessionOf(r).User, p)
	case "delete":
		s.confirmDelete(w, r)
	case "delete-confirm":
		s.deleteItems(w, r)
	default:
		s.errorPage(w, r, http.StatusBadRequest, "error.method")
	}
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

// healthz answers 200 without a session and reveals nothing about the configuration.
func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok\n"))
}
