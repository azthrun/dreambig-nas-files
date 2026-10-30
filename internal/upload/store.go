package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
)

// filePerm is the mode of staged files, and so of completed ones (same inode).
const filePerm = 0o644

// errInternal is what a User sees for any unexpected failure: tusd would otherwise echo
// the error text, which may contain an absolute NAS path.
var errInternal = handler.NewError("ERR_INTERNAL_SERVER_ERROR", "internal error", http.StatusInternalServerError)

// store is tusd's data store over every writable Mount's Staging Area. Each Mount keeps
// its Upload data in <mount>/.nas-files-staging, on the Mount's own filesystem, so the
// completed file can be hard-linked into place. A new Upload goes to the Mount named in
// its (server-set) metadata; an existing one is found by scanning the Staging Areas.
//
// It is the seam for #14: expiry lists and terminates what each Staging Area holds, and
// the free-space reserve is checked in NewUpload.
type store struct {
	mounts map[string]mounts.Mount // writable Mounts by name
	order  []string                // names, for stable scans
	log    *slog.Logger
}

func newStore(ms []mounts.Mount, log *slog.Logger) *store {
	s := &store{mounts: map[string]mounts.Mount{}, log: log}
	for _, m := range ms {
		if !m.ReadOnly {
			s.mounts[m.Name] = m
			s.order = append(s.order, m.Name)
		}
	}
	return s
}

// stagingDir returns a Mount's Staging Area, creating it when create is set. A symlink or
// file standing in its place is refused.
func stagingDir(m mounts.Mount, create bool) (string, error) {
	dir := filepath.Join(m.Path, mounts.StagingDir)
	if create {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create staging area: %w", err)
		}
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", errors.New("staging area is not a folder")
	}
	return dir, nil
}

func (s *store) files(m mounts.Mount, create bool) (filestore.FileStore, error) {
	dir, err := stagingDir(m, create)
	if err != nil {
		return filestore.FileStore{}, err
	}
	fs := filestore.New(dir)
	fs.DirModePerm, fs.FileModePerm = 0o700, filePerm
	return fs, nil
}

func (s *store) NewUpload(ctx context.Context, info handler.FileInfo) (handler.Upload, error) {
	m, ok := s.mounts[info.MetaData[metaMount]]
	if !ok {
		return nil, errInternal // preCreate always sets a writable Mount
	}
	fs, err := s.files(m, true)
	if err != nil {
		return nil, s.scrub(ctx, "staging: new upload", err)
	}
	u, err := fs.NewUpload(ctx, info)
	if err != nil {
		return nil, s.scrub(ctx, "staging: new upload", err)
	}
	return &stagedUpload{Upload: u, fs: fs, s: s}, nil
}

func (s *store) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	for _, name := range s.order {
		fs, err := s.files(s.mounts[name], false)
		if err != nil {
			continue // no Staging Area yet in this Mount
		}
		u, err := fs.GetUpload(ctx, id)
		if errors.Is(err, handler.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, s.scrub(ctx, "staging: get upload", err)
		}
		return &stagedUpload{Upload: u, fs: fs, s: s, mount: name}, nil
	}
	return nil, handler.ErrNotFound
}

func (s *store) AsTerminatableUpload(u handler.Upload) handler.TerminatableUpload {
	return u.(*stagedUpload)
}

// scrub passes tusd's own errors (they carry a status and a safe message) and replaces
// anything else with errInternal, logging the real error.
func (s *store) scrub(ctx context.Context, what string, err error) error {
	var tusErr handler.Error
	if errors.As(err, &tusErr) {
		return err
	}
	s.log.ErrorContext(ctx, what, "err", err)
	return errInternal
}

// stagedUpload is one Upload's data in a Staging Area.
type stagedUpload struct {
	handler.Upload
	fs    filestore.FileStore
	s     *store
	mount string
}

func (u *stagedUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	n, err := u.Upload.WriteChunk(ctx, offset, src)
	if err != nil {
		err = u.s.scrub(ctx, "staging: write", err)
	}
	return n, err
}

// Terminate removes the Upload's staged data and info: Staging Area data is one of the
// few things the service ever removes (CODING_STANDARDS.md).
func (u *stagedUpload) Terminate(ctx context.Context) error {
	if err := u.fs.AsTerminatableUpload(u.Upload).Terminate(ctx); err != nil {
		return u.s.scrub(ctx, "staging: terminate", err)
	}
	return nil
}
