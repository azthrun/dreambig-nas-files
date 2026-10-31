package server

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/scrub"
	"github.com/azthrun/dreambig-nas-files/internal/trash"
)

// refusals is THE mapping from sentinel errors to the HTTP status and the i18n key of the
// error page. Every handler answers a failed request through refuse, which reads this
// table; nothing else may pick a status for an error. Order matters: the first match
// wins, so a more specific error (an invalid name is also an invalid Path) comes first.
//
// Hidden Entries, escapes and a Mount without Access all look exactly like not-found
// (404), so nothing about them can be probed.
var refusals = []struct {
	target error
	status int
	key    string
}{
	{errNothingSelected, http.StatusBadRequest, "error.no_selection"},
	{resolver.ErrInvalidName, http.StatusBadRequest, "error.bad_name"},
	{resolver.ErrInvalidPath, http.StatusBadRequest, "error.bad_path"},
	{trash.ErrInvalid, http.StatusBadRequest, "error.bad_path"},
	{errForbidden, http.StatusForbidden, "error.no_write"},
	{trash.ErrReadOnly, http.StatusForbidden, "error.no_write"},
	{resolver.ErrNotFound, http.StatusNotFound, "error.not_found"}, // includes errNoAccess
	{resolver.ErrHidden, http.StatusNotFound, "error.not_found"},
	{resolver.ErrOutsideRoot, http.StatusNotFound, "error.not_found"},
	{resolver.ErrNotFolder, http.StatusNotFound, "error.not_found"},
	{errNotFile, http.StatusNotFound, "error.not_found"},
	{trash.ErrNotFound, http.StatusNotFound, "error.not_found"},
	{fs.ErrNotExist, http.StatusNotFound, "error.not_found"},
	{errExists, http.StatusConflict, "error.exists"},
}

// refusal is how one error is answered.
type refusal struct {
	Status int
	Key    string
}

// refusalOf looks err up in refusals; anything unknown is an internal error.
func refusalOf(err error) refusal {
	for _, r := range refusals {
		if errors.Is(err, r.target) {
			return refusal{r.status, r.key}
		}
	}
	return refusal{http.StatusInternalServerError, "error.internal"}
}

// statusOf is the HTTP status for err (the Upload handler uses it too).
func statusOf(err error) int { return refusalOf(err).Status }

// refuse audits and answers a request that failed with err. path is the Path the audit
// line names; it is left out when the request string was not a valid Path. A plain miss
// (nothing at a valid Path in a Mount the User can see) is routine navigation and is not
// audited when routineMiss is set; every other refusal is, including a Mount without
// Access, which answers like a missing Path but is worth knowing about. An unexpected
// error is logged, audited as failed, and shown as a generic internal error.
func (s *server) refuse(w http.ResponseWriter, r *http.Request, op, user, path string, err error, routineMiss bool) {
	ref := refusalOf(err)
	ev := audit.Event{User: user, Operation: op, Outcome: audit.Refused, Status: ref.Status}
	if !errors.Is(err, resolver.ErrInvalidPath) || errors.Is(err, resolver.ErrInvalidName) {
		ev.Path = path // only a syntactically valid Path is ever recorded
	}
	switch {
	case ref.Status == http.StatusInternalServerError:
		s.Log.Error(op, "err", scrub.Path(err))
		ev.Outcome = audit.Failed
	case routineMiss && isPlainMiss(err):
		s.errorPage(w, r, ref.Status, ref.Key)
		return
	}
	s.Audit.Record(ev)
	s.errorPage(w, r, ref.Status, ref.Key)
}

// isPlainMiss is a valid Path that names nothing, as opposed to one the User may not
// know about (no Access).
func isPlainMiss(err error) bool {
	return (errors.Is(err, resolver.ErrNotFound) || errors.Is(err, resolver.ErrNotFolder)) && !errors.Is(err, errNoAccess)
}
