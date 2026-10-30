package server

import (
	"net/http"

	"github.com/azthrun/dreambig-nas-files/internal/web"
)

// routes is the one place routes are registered.
//
// Public routes (no session) are only /healthz, sign-in (GET and POST /login) and
// /static (the CSS the sign-in page itself needs). Every other route MUST be registered
// with s.protected, which puts the session and CSRF checks in front of it.
//
// File routes must not use ServeMux path cleaning (it redirects ".." instead of letting
// the Path resolver refuse it); register a subtree pattern and let the handler parse
// r.URL.EscapedPath().
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web.Static())))

	mux.Handle("GET /{$}", s.protected(s.root))
	mux.Handle("POST /logout", s.protected(s.logout))
	return s.wrap(mux)
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
