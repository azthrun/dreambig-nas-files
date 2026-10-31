// Package server is the web layer: routes, middleware and handlers. It is built from
// Deps so tests can wire the whole application around temp folders and a fake clock.
package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"html/template"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/identity"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
	"github.com/azthrun/dreambig-nas-files/internal/upload"
	"github.com/azthrun/dreambig-nas-files/internal/web"
)

// Deps is everything the application needs from outside. main and testenv are the only
// constructors.
type Deps struct {
	// Mounts are the validated Mounts from mounts.Discover. At least one.
	Mounts []mounts.Mount
	// Clock is the time source; defaults to the wall clock.
	Clock clock.Clock
	// Auth verifies sign-ins (the htpasswd file in production). Required.
	Auth identity.Authenticator
	// Access says what each User may do in each Mount. Required.
	Access *access.Table
	// Trash puts deleted content in each Mount's Trash. Required. main starts its purge
	// loop; the server only places content.
	Trash *trash.Trash
	// Uploads applies the Upload limits (free-space reserve, expiry). Optional: without
	// one, new Uploads are never refused for space and nothing expires. main runs its sweep.
	Uploads *upload.Keeper
	// Audit receives one JSON line per auditable event (stdout in production). Share
	// this one Logger with the Trash and the Upload Keeper, so lines never interleave.
	Audit *audit.Logger
	// Log receives operational logs (stderr in production); defaults to discarding.
	Log *slog.Logger
}

type server struct {
	Deps
	audit    *audit.Logger // s.audit.Record(audit.Event{...}) is the one audit API
	sessions *sessions
	limiter  *limiter
	tmpl     *template.Template
	// res is the Path resolver: every request-supplied Path goes through it.
	res *resolver.Resolver
	// up serves resumable Uploads (tus) under /uploads/.
	up *upload.Handler
}

// New builds the application handler.
func New(d Deps) (http.Handler, error) {
	if len(d.Mounts) == 0 {
		return nil, errors.New("server: no Mounts")
	}
	if d.Auth == nil {
		return nil, errors.New("server: no authenticator")
	}
	if d.Access == nil {
		return nil, errors.New("server: no Access")
	}
	if d.Trash == nil {
		return nil, errors.New("server: no Trash")
	}
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Audit == nil {
		d.Audit = audit.New(io.Discard, d.Clock)
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	tmpl, err := web.Templates()
	if err != nil {
		return nil, err
	}
	res, err := resolver.New(d.Mounts)
	if err != nil {
		return nil, err
	}
	s := &server{
		res:      res,
		Deps:     d,
		audit:    d.Audit,
		sessions: newSessions(d.Clock),
		limiter:  newLimiter(d.Clock),
		tmpl:     tmpl,
	}
	up, err := upload.New(upload.Config{
		Mounts: d.Mounts, Resolver: res, Trash: d.Trash, Audit: s.audit, Log: d.Log, StatusOf: statusOf, Keeper: d.Uploads,
		Authorize: func(user string, loc resolver.Location) error { return s.authorize(user, loc, access.Write) },
	})
	if err != nil {
		return nil, err
	}
	s.up = up
	return s.routes(), nil
}
