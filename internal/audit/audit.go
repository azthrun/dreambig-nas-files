// Package audit writes the audit log: one JSON line on stdout per auditable event
// (sign-in, write, purge, refusal), naming the User and the Path.
//
// This is the one API every ticket uses:
//
//	s.Audit.Record(audit.Event{User: u.Name, Operation: "delete", Path: p, Outcome: audit.OK, Status: 200})
//
// Operations are short lowercase verbs ("signin", "signout", "delete", "upload", ...).
// Refused attempts use Outcome audit.Refused with the HTTP status that was sent.
package audit

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
)

// Outcome says how an audited operation ended.
type Outcome string

const (
	// OK means the operation happened.
	OK Outcome = "ok"
	// Refused means the service declined it (bad credentials, no session, no CSRF
	// token, no Access, a rule broken).
	Refused Outcome = "refused"
	// Failed means it was allowed but went wrong.
	Failed Outcome = "failed"
)

// Event is one auditable thing that happened. Empty fields are omitted from the line.
type Event struct {
	// User is the signed-in User, or the name that was claimed in a failed sign-in.
	User      string
	Operation string
	// Path is a Path relative to the Root, never an absolute NAS path.
	Path    string
	Outcome Outcome
	// Status is the HTTP status sent.
	Status int
	Bytes  int64
	// Choice is the Conflict choice the User made (skip, keep, replace), when there was one.
	Choice string
	// Requested is the Path the User asked for when the operation ended up at a different
	// one (a kept-both Upload lands at "name (1).ext"); Path is where it really is. Empty
	// when they are the same.
	Requested string
}

type line struct {
	Time      string  `json:"time"`
	User      string  `json:"user,omitempty"`
	Operation string  `json:"operation"`
	Path      string  `json:"path,omitempty"`
	Outcome   Outcome `json:"outcome"`
	Status    int     `json:"status,omitempty"`
	Bytes     int64   `json:"bytes,omitempty"`
	Choice    string  `json:"choice,omitempty"`
	Requested string  `json:"requested,omitempty"`
}

// Logger writes audit lines. Safe for concurrent use.
type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	clk clock.Clock
}

// New returns a Logger writing to w, timestamping from clk.
func New(w io.Writer, clk clock.Clock) *Logger {
	return &Logger{w: w, clk: clk}
}

// Record writes e as one JSON line. JSON escaping keeps each line single-line even when
// a value contains newlines.
func (l *Logger) Record(e Event) {
	b, err := json.Marshal(line{
		Time:      l.clk.Now().UTC().Format(time.RFC3339),
		User:      e.User,
		Operation: e.Operation,
		Path:      e.Path,
		Outcome:   e.Outcome,
		Status:    e.Status,
		Bytes:     e.Bytes,
		Choice:    e.Choice,
		Requested: e.Requested,
	})
	if err != nil {
		return // cannot happen for these field types
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}
