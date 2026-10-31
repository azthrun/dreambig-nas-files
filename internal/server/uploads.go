package server

import "net/http"

// uploads serves the tus endpoint. It runs behind s.protected, so every request already
// has a session and every non-GET/HEAD one a valid CSRF token; the Upload handler then
// checks Access and name rules on each request that names a Path and ownership on every
// request that names an Upload.
func (s *server) uploads(w http.ResponseWriter, r *http.Request) {
	s.up.ServeHTTP(w, r, sessionOf(r).User)
}
