// Package server is the web layer: routes, middleware and handlers. It is built from
// Deps so tests can wire the whole application around temp folders and a fake clock.
package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
)

// Deps is everything the application needs from outside. Later tickets add fields
// (users, Access, free-space probe, ...); main and testenv are the only constructors.
type Deps struct {
	// Mounts are the validated Mounts from mounts.Discover. At least one.
	Mounts []mounts.Mount
	// Clock is the time source; defaults to the wall clock.
	Clock clock.Clock
	// Audit receives one JSON line per auditable event (stdout in production).
	Audit io.Writer
	// Log receives operational logs (stderr in production); defaults to discarding.
	Log *slog.Logger
}

type server struct {
	Deps
}

// New builds the application handler.
func New(d Deps) (http.Handler, error) {
	if len(d.Mounts) == 0 {
		return nil, errors.New("server: no Mounts")
	}
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Audit == nil {
		d.Audit = io.Discard
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &server{Deps: d}
	return s.routes(), nil
}
