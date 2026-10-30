package server

import (
	"net/http"
)

// routes is the one place routes are registered.
//
// Public routes (no session) are only /healthz and, from ticket #3, sign-in. Every other
// route must be registered with s.protected, which puts the session and CSRF middleware
// in front of it. #3 adds that wrapper; until then there are no other routes, so there is
// deliberately no pass-through placeholder that could be mistaken for protection.
//
// File routes must not use ServeMux path cleaning (it redirects ".." instead of letting
// the Path resolver refuse it); register a subtree pattern and let the handler parse
// r.URL.EscapedPath().
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	return s.wrap(mux)
}

// wrap applies middleware common to every route. Later tickets add request audit
// logging and security headers here.
func (s *server) wrap(next http.Handler) http.Handler {
	return next
}

// healthz answers 200 without a session and reveals nothing about the configuration.
func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok\n"))
}
