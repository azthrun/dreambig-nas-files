// Package upload serves resumable Uploads over tus through embedded tusd (file-api ADR-0003,
// adapted: per-Mount Staging Areas, session and CSRF instead of a Bearer token).
//
// An Upload's data waits in the Staging Area of the Mount it will land in
// (<mount>/.nas-files-staging, a Hidden Entry) until the last byte arrives; then the
// Placer puts it at its Path. Partial data is never at the Path, never listed and never
// downloadable.
//
// tus Upload-Metadata the browser sends:
//
//	path      the destination Path relative to the Root ("docs/tax/2025.pdf"); required
//	conflict  what to do if a file is at the Path: "keep" (next free "name (1).ext") or
//	          "replace" (the old file goes to the Trash); absent means refuse. Skip is
//	          never sent: the browser just does not upload.
//
// Everything else the client sends is dropped. tusd's metadata is client-supplied, so the
// path is parsed, resolved and Access-checked at creation and again at completion; the
// server stores the creating User and the real Mount next to it, and only that User may
// touch the Upload.
package upload

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
	expslog "golang.org/x/exp/slog"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// BasePath is where the tus endpoint is mounted.
const BasePath = "/uploads/"

// Metadata keys. Only metaPath comes from the client; the others are set by the server.
const (
	metaPath  = "path"
	metaUser  = "user"
	metaMount = "mount"
	// metaConflict is client-supplied and whitelisted in preCreate.
	metaConflict = "conflict"
)

// idRe matches the IDs tusd's filestore generates (128 random bits, hex).
var idRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

var (
	errConflict = handler.NewError("ERR_CONFLICT", "a file already exists at that Path", http.StatusConflict)
	// errConflictFolder: a folder is at the Path; only skip is possible.
	errConflictFolder = handler.NewError("ERR_CONFLICT_FOLDER", "a folder is at that Path", http.StatusConflict)
	// errConflictTrashed: a late Conflict during replace; the old file is in the Trash.
	errConflictTrashed = handler.NewError("ERR_CONFLICT_TRASHED", "a file appeared at that Path; the replaced file stays in the Trash", http.StatusConflict)
	errBadChoice       = handler.NewError("ERR_INVALID_CHOICE", "unknown Conflict choice", http.StatusBadRequest)
	errBadPath         = handler.NewError("ERR_INVALID_PATH", "invalid path or name", http.StatusBadRequest)
	errForbidden       = handler.NewError("ERR_FORBIDDEN", "not allowed", http.StatusForbidden)
	errNotFound        = handler.NewError("ERR_NOT_FOUND", "not found", http.StatusNotFound)
	errNoUpload        = handler.NewError("ERR_UPLOAD_NOT_FOUND", "upload not found", http.StatusNotFound)
	errMethod          = handler.NewError("ERR_METHOD_NOT_ALLOWED", "method not allowed", http.StatusMethodNotAllowed)
)

// Config wires a Handler.
type Config struct {
	// Mounts are all Mounts; each writable one gets a Staging Area on first use.
	Mounts []mounts.Mount
	// Resolver is the Path resolver every destination goes through.
	Resolver *resolver.Resolver
	// Authorize is the Access check for writing to loc (the server's authorize with
	// access.Write); StatusOf maps its errors, and the resolver's, to an HTTP status.
	Authorize func(user string, loc resolver.Location) error
	StatusOf  func(error) int
	// Audit receives one line per create, completion, cancel and refusal.
	Audit *audit.Logger
	// Log receives operational errors; nil discards them.
	Log *slog.Logger
	// Trash is where Replace puts the old file; it must be the server's Trash so deletes
	// and replacements share one write lock. Nil disables replace.
	Trash Trasher
	// Place completes an Upload; nil means a ChoicePlacer over Trash.
	Place Placer
}

// Handler serves the tus endpoint. Mount it behind the session and CSRF middleware and
// call ServeHTTP with the signed-in User.
type Handler struct {
	Config
	store *store
	tus   *handler.UnroutedHandler

	// pending holds Uploads whose data is complete but waits for the User to settle a
	// late Conflict (see preFinish). In memory: after a restart the User uploads again.
	pmu     sync.Mutex
	pending map[string]bool
}

// New builds a Handler.
func New(cfg Config) (*Handler, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Place == nil {
		cfg.Place = ChoicePlacer{Trash: cfg.Trash, Log: cfg.Log}
	}
	h := &Handler{Config: cfg, store: newStore(cfg.Mounts, cfg.Log), pending: map[string]bool{}}

	composer := handler.NewStoreComposer()
	composer.UseCore(h.store)
	composer.UseTerminater(h.store)
	composer.UseLocker(memorylocker.New())

	tus, err := handler.NewUnroutedHandler(handler.Config{
		StoreComposer:        composer,
		BasePath:             BasePath,
		DisableDownload:      true, // Uploads are never readable; downloads have their own route
		DisableConcatenation: true,
		// The page is same-origin, so no CORS headers are wanted at all.
		Cors:                      &handler.CorsConfig{Disable: true},
		PreUploadCreateCallback:   h.preCreate,
		PreFinishResponseCallback: h.preFinish,
		// tusd logs every request; the audit log covers that.
		Logger: expslog.New(expslog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, err
	}
	h.tus = tus
	return h, nil
}

type ctxKey struct{}

// reqState is what hooks and the audit line share for one request.
type reqState struct {
	user      string
	path      string // a syntactically valid Path only
	completed bool
	size      int64
	denied    bool // somebody else's Upload
	choice    string
	op        string // audit operation, when not the usual one for the method
}

func stateOf(ctx context.Context) *reqState {
	s, _ := ctx.Value(ctxKey{}).(*reqState)
	if s == nil {
		s = &reqState{}
	}
	return s
}

// ServeHTTP serves BasePath (create) and BasePath/<id> (status, resume, cancel) for user.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, user string) {
	id := strings.TrimPrefix(r.URL.Path, BasePath)
	if r.URL.Path == strings.TrimSuffix(BasePath, "/") {
		id = ""
	}
	resolve := strings.HasSuffix(id, "/resolve")
	id = strings.TrimSuffix(id, "/resolve")
	st := &reqState{user: user}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	body := &countingReader{r: r.Body}
	// tusd takes the Upload ID to be the whole request path.
	r2 := r.Clone(context.WithValue(r.Context(), ctxKey{}, st))
	r2.URL.Path, r2.URL.RawPath = "/"+id, ""
	if r.Body != nil {
		r2.Body = body
	}
	switch {
	case id != "" && !idRe.MatchString(id):
		writeTusError(rec, errNoUpload)
	case id == "" && resolve:
		writeTusError(rec, errNoUpload)
	case id == "":
		if r.Method != http.MethodPost {
			rec.Header().Set("Allow", "POST")
			writeTusError(rec, errMethod)
		} else {
			h.tus.PostFile(rec, r2)
		}
	case resolve:
		h.resolve(rec, r2, id, st)
	default:
		h.item(rec, r2, id, st)
	}
	h.record(r.Method, rec.status, st, body.n)
}

// item serves HEAD, PATCH and DELETE of one Upload, for its creator only. To anyone else
// an Upload does not exist.
func (h *Handler) item(w http.ResponseWriter, r *http.Request, id string, st *reqState) {
	var serve http.HandlerFunc
	switch r.Method {
	case http.MethodHead:
		serve = h.tus.HeadFile
	case http.MethodPatch:
		serve = h.tus.PatchFile
	case http.MethodDelete:
		serve = h.tus.DelFile
	default:
		w.Header().Set("Allow", "HEAD, PATCH, DELETE")
		writeTusError(w, errMethod)
		return
	}
	u, err := h.store.GetUpload(r.Context(), id)
	if err != nil {
		writeTusError(w, errNoUpload)
		return
	}
	info, err := u.GetInfo(r.Context())
	if err != nil || info.MetaData[metaUser] != st.user {
		st.denied = true
		writeTusError(w, errNoUpload)
		return
	}
	if p, err := resolver.Parse(info.MetaData[metaPath]); err == nil {
		st.path = p.String()
	}
	if r.Method == http.MethodHead && h.isPending(id) {
		// Its data is complete but it never landed. A client that saw "complete" would
		// report success, so until the User settles the Conflict it does not resume.
		writeTusError(w, errNoUpload)
		return
	}
	if r.Method == http.MethodDelete {
		h.setPending(id, false)
	}
	serve(w, r)
}

func (h *Handler) isPending(id string) bool {
	h.pmu.Lock()
	defer h.pmu.Unlock()
	return h.pending[id]
}

func (h *Handler) setPending(id string, on bool) {
	h.pmu.Lock()
	defer h.pmu.Unlock()
	if on {
		h.pending[id] = true
	} else {
		delete(h.pending, id)
	}
}

// resolve settles a late Conflict: POST /uploads/<id>/resolve?choice=skip|keep|replace.
// The Upload's data was kept when the last byte met a Conflict; skip discards it, keep
// and replace place it with the User's choice (a Conflict can still come back). Only the
// creator can resolve, and only an Upload that is waiting for it.
func (h *Handler) resolve(w http.ResponseWriter, r *http.Request, id string, st *reqState) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeTusError(w, errMethod)
		return
	}
	ctx := r.Context()
	u, err := h.store.GetUpload(ctx, id)
	if err != nil {
		writeTusError(w, errNoUpload)
		return
	}
	info, err := u.GetInfo(ctx)
	if err != nil || info.MetaData[metaUser] != st.user {
		st.denied = true
		writeTusError(w, errNoUpload)
		return
	}
	if p, err := resolver.Parse(info.MetaData[metaPath]); err == nil {
		st.path = p.String()
	}
	if !h.isPending(id) {
		writeTusError(w, errNoUpload)
		return
	}
	choice := r.URL.Query().Get("choice")
	st.choice = choice
	switch choice {
	case ChoiceSkip:
		st.op = "upload_skip"
		if err := h.store.AsTerminatableUpload(u).Terminate(ctx); err != nil {
			h.Log.ErrorContext(ctx, "discard skipped upload", "id", id, "err", err)
			writeTusError(w, errInternal)
			return
		}
		h.setPending(id, false)
		w.Header().Set("Tus-Resumable", "1.0.0")
		w.WriteHeader(http.StatusNoContent)
		return
	case ChoiceKeep, ChoiceReplace:
	default:
		writeTusError(w, errBadChoice)
		return
	}
	st.op = "upload"
	if err := h.complete(ctx, st, u, info, choice); err != nil {
		if !isConflict(err) {
			h.setPending(id, false)
		}
		writeTusError(w, h.tusError(ctx, err))
		return
	}
	h.setPending(id, false)
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.WriteHeader(http.StatusNoContent)
}

// isConflict is true for the errors that mean "the Path is not free": the staged data is
// kept so the User can decide.
func isConflict(err error) bool {
	return errors.Is(err, ErrConflict) || errors.Is(err, ErrConflictFolder)
}

// record writes the audit line for a finished request: creation, completion, cancel, and
// every refusal or failure. Progress PATCHes and routine HEADs are not audited.
func (h *Handler) record(method string, status int, st *reqState, bytes int64) {
	ev := audit.Event{User: st.user, Path: st.path, Status: status, Choice: st.choice}
	switch {
	case status >= 500:
		ev.Outcome = audit.Failed
	case status >= 400:
		ev.Outcome = audit.Refused
	default:
		ev.Outcome = audit.OK
	}
	switch method {
	case http.MethodPost:
		ev.Operation = "upload_create"
		if st.op != "" { // a resolve: the outcome is the completion
			ev.Bytes = st.size
		}
	case http.MethodPatch:
		ev.Operation = "upload"
		if st.completed {
			ev.Bytes = st.size
		} else if status < 400 {
			return
		}
	case http.MethodDelete:
		ev.Operation = "upload_cancel"
	default:
		if status < 400 || !st.denied {
			return
		}
		ev.Operation = "upload_status"
	}
	if st.op != "" {
		ev.Operation = st.op
	}
	h.Audit.Record(ev)
}

// check validates a destination the client named and returns where it would land. It is
// run at creation and again at completion: parse, name rules, resolve, Access, and the
// Conflict and parent-folder checks. #13 relaxes the parent check for Folder Uploads.
func (h *Handler) check(user, raw, choice string) (resolver.Path, resolver.Location, error) {
	p, err := resolver.Parse(raw)
	if err != nil {
		return resolver.Path{}, resolver.Location{}, err
	}
	if p.IsRoot() {
		return p, resolver.Location{}, resolver.ErrInvalidPath
	}
	if _, err := resolver.ValidateName(p.Name()); err != nil {
		return p, resolver.Location{}, err
	}
	loc, err := h.Resolver.Resolve(p)
	if err != nil {
		return p, loc, err
	}
	if err := h.Authorize(user, loc); err != nil {
		return p, loc, err
	}
	if _, ok := h.store.mounts[loc.Mount.Name]; !ok {
		return p, loc, resolver.ErrNotFound // a read-only Mount has no Staging Area
	}
	if loc.Exists {
		// Replace and keep both apply to files only; whatever is not a file is a folder
		// (or a link to one) and only skip is possible.
		if fi, err := loc.Stat(); err == nil && fi.IsDir() {
			return p, loc, ErrConflictFolder
		}
		if choice == "" {
			return p, loc, ErrConflict
		}
		return p, loc, nil // the Placer applies the choice
	}
	parent, err := h.Resolver.Resolve(p.Parent())
	if err != nil {
		return p, loc, err
	}
	if fi, err := parent.Stat(); err != nil || !fi.IsDir() {
		return p, loc, resolver.ErrNotFound
	}
	return p, loc, nil
}

// preCreate validates a new Upload and replaces the client's metadata with the server's.
// Of everything the client sent, only the path and the Conflict choice survive, and the
// choice only if it is one of the known ones.
func (h *Handler) preCreate(ev handler.HookEvent) (handler.HTTPResponse, handler.FileInfoChanges, error) {
	st := stateOf(ev.Context)
	choice := ev.Upload.MetaData[metaConflict]
	st.choice = choice
	if choice != "" && choice != ChoiceKeep && choice != ChoiceReplace {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, errBadChoice
	}
	p, loc, err := h.check(st.user, ev.Upload.MetaData[metaPath], choice)
	if err == nil || !errors.Is(err, resolver.ErrInvalidPath) {
		st.path = p.String()
	}
	if err != nil {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, h.tusError(ev.Context, err)
	}
	md := handler.MetaData{metaPath: p.String(), metaUser: st.user, metaMount: loc.Mount.Name}
	if choice != "" {
		md[metaConflict] = choice
	}
	return handler.HTTPResponse{}, handler.FileInfoChanges{MetaData: md}, nil
}

// preFinish completes an Upload whose last byte has arrived. Everything is checked again
// (a late Conflict is possible), then the Placer puts the data at its Path. On a late
// Conflict the staged data is kept and the User decides (resolve); on any other failure
// it is discarded, so a retry cannot find a "complete" Upload that never landed.
func (h *Handler) preFinish(ev handler.HookEvent) (handler.HTTPResponse, error) {
	ctx := ev.Context
	st := stateOf(ctx)
	u, err := h.store.GetUpload(ctx, ev.Upload.ID)
	if err != nil {
		return handler.HTTPResponse{}, err
	}
	choice := ev.Upload.MetaData[metaConflict]
	st.choice = choice
	if err := h.complete(ctx, st, u, ev.Upload, choice); err != nil {
		if isConflict(err) {
			h.setPending(ev.Upload.ID, true)
		} else if terr := h.store.AsTerminatableUpload(u).Terminate(ctx); terr != nil {
			h.Log.ErrorContext(ctx, "discard staged upload", "id", ev.Upload.ID, "err", terr)
		}
		return handler.HTTPResponse{}, h.tusError(ctx, err)
	}
	return handler.HTTPResponse{}, nil
}

// complete re-checks a finished Upload, places it with choice and removes its staged
// name. On error nothing has been done to the staged data.
func (h *Handler) complete(ctx context.Context, st *reqState, u handler.Upload, info handler.FileInfo, choice string) error {
	p, loc, err := h.check(st.user, info.MetaData[metaPath], choice)
	if err == nil && (info.MetaData[metaUser] != st.user || loc.Mount.Name != info.MetaData[metaMount]) {
		err = resolver.ErrNotFound // the Path now leads into another Mount
	}
	st.path = p.String()
	if err != nil {
		return err
	}
	if err := h.Place.Place(ctx, Placement{User: st.user, Dest: loc, Staged: stagedName(info.ID), Choice: choice}); err != nil {
		return err
	}
	// The file is in place; failing to drop the staged name only leaves a link for expiry.
	if err := h.store.AsTerminatableUpload(u).Terminate(ctx); err != nil {
		h.Log.ErrorContext(ctx, "remove staged upload after completion", "id", info.ID, "err", err)
	}
	st.completed, st.size = true, info.Size
	return nil
}

// tusError turns a resolver, Access or Placer error into a tus error without NAS paths.
func (h *Handler) tusError(ctx context.Context, err error) error {
	var te handler.Error
	if errors.As(err, &te) {
		return err
	}
	var kept replacedKept
	switch {
	case errors.As(err, &kept):
		return errConflictTrashed
	case errors.Is(err, ErrConflictFolder):
		return errConflictFolder
	case errors.Is(err, ErrConflict):
		return errConflict
	}
	switch status := h.StatusOf(err); status {
	case http.StatusBadRequest:
		return errBadPath
	case http.StatusForbidden:
		return errForbidden
	case http.StatusNotFound:
		return errNotFound
	}
	h.Log.ErrorContext(ctx, "upload", "err", err)
	return errInternal
}

func writeTusError(w http.ResponseWriter, err error) {
	var te handler.Error
	if !errors.As(err, &te) {
		te = errInternal
	}
	for k, v := range te.HTTPResponse.Header {
		w.Header().Set(k, v)
	}
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.WriteHeader(te.HTTPResponse.StatusCode)
	_, _ = io.WriteString(w, te.HTTPResponse.Body)
}

// statusRecorder remembers the status for the audit line. Unwrap lets tusd reach the
// underlying ResponseWriter for flushing and deadlines.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

type countingReader struct {
	r io.ReadCloser
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.r == nil {
		return 0, io.EOF
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReader) Close() error {
	if c.r == nil {
		return nil
	}
	return c.r.Close()
}
