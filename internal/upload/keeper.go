package upload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tus/tusd/v2/pkg/handler"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/scrub"
)

// infoSuffix is the extension of tusd's per-Upload info file.
const infoSuffix = ".info"

// errInsufficientSpace refuses a new Upload that would eat into the free-space reserve.
// The code is stable; the page shows its own translated text for it (upload.js).
var errInsufficientSpace = handler.NewError("ERR_INSUFFICIENT_SPACE",
	"not enough free space for this Upload", http.StatusInsufficientStorage)

// KeeperConfig wires a Keeper.
type KeeperConfig struct {
	Mounts []mounts.Mount
	// Clock is the time source for activity marks and expiry; nil means the wall clock.
	Clock clock.Clock
	// Expiry is how long an incomplete Upload may sit idle before it is removed; 0 keeps
	// them forever.
	Expiry time.Duration
	// Reserve is the free space, in bytes, that must remain on a Mount's filesystem
	// after a new Upload's declared length; 0 disables the check.
	Reserve int64
	// FreeSpace reports the bytes available on the filesystem holding m; nil asks the
	// filesystem. Tests substitute a fake.
	FreeSpace func(m mounts.Mount) (int64, error)
	Audit     *audit.Logger
	Log       *slog.Logger
}

// Keeper owns the limits on Uploads: the free-space reserve for new ones and the expiry
// of abandoned ones. The Upload Handler asks it before creating an Upload, takes its
// locks and reports activity to it; main runs its sweep.
type Keeper struct {
	cfg    KeeperConfig
	locker *locker
}

// NewKeeper builds a Keeper.
func NewKeeper(cfg KeeperConfig) *Keeper {
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.FreeSpace == nil {
		cfg.FreeSpace = statfsFree
	}
	return &Keeper{cfg: cfg, locker: newLocker()}
}

// statfsFree is the bytes an unprivileged writer may still use on m's filesystem.
func statfsFree(m mounts.Mount) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.Path, &st); err != nil {
		return 0, fmt.Errorf("statfs: %w", scrub.Path(err))
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if free > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(free), nil
}

// admit refuses a new Upload of size bytes into m when the Mount's filesystem would be
// left with less than the reserve. Only new Uploads are judged; one under way finishes.
func (k *Keeper) admit(m mounts.Mount, size int64) error {
	if k.cfg.Reserve <= 0 {
		return nil
	}
	free, err := k.cfg.FreeSpace(m)
	if err != nil {
		return fmt.Errorf("free space: %w", err)
	}
	if free-k.cfg.Reserve < size {
		return errInsufficientSpace
	}
	return nil
}

// touch marks the Upload id in the Staging Area dir as active now, by the Keeper's
// clock. The mtimes on disk are the only record of activity, so expiry survives restarts.
func (k *Keeper) touch(dir, id string) {
	now := k.cfg.Clock.Now()
	for _, name := range []string{id, id + infoSuffix} {
		if err := os.Chtimes(filepath.Join(dir, name), now, now); err != nil && !errors.Is(err, fs.ErrNotExist) {
			k.cfg.Log.Error("touch staged upload", "err", scrub.Path(err))
		}
	}
}

// Sweep removes every abandoned Upload from the Staging Area of every writable Mount:
// those whose data and info were last touched more than Expiry ago, including ones left
// by earlier runs and data whose info is gone. An Upload a request holds is skipped,
// never interrupted. Only Staging Area data is removed. Each removal, and each failure,
// is one audit line (operation "upload_expire"). Read-only Mounts have no Staging Area.
func (k *Keeper) Sweep(ctx context.Context) {
	if k.cfg.Expiry <= 0 {
		return
	}
	for _, m := range k.cfg.Mounts {
		if m.ReadOnly || ctx.Err() != nil {
			continue
		}
		dir, err := stagingDir(m, false)
		if err != nil {
			continue // no Staging Area yet
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			k.cfg.Log.Error("expire uploads: list staging area", "mount", m.Name, "err", scrub.Path(err))
			continue
		}
		ids := map[string]bool{}
		for _, de := range ents {
			if id := strings.TrimSuffix(de.Name(), infoSuffix); idRe.MatchString(id) && de.Type().IsRegular() {
				ids[id] = true
			}
		}
		for id := range ids {
			if ctx.Err() != nil {
				return
			}
			k.expireOne(m, dir, id)
		}
	}
}

// idle reports whether every present file of Upload id was last modified more than
// Expiry ago.
func (k *Keeper) idle(dir, id string) bool {
	cutoff := k.cfg.Clock.Now().Add(-k.cfg.Expiry)
	found := false
	for _, name := range []string{id, id + infoSuffix} {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		found = true
		if !fi.ModTime().Before(cutoff) {
			return false
		}
	}
	return found
}

func (k *Keeper) expireOne(m mounts.Mount, dir, id string) {
	if !k.idle(dir, id) {
		return
	}
	unlock, ok := k.locker.tryLock(id)
	if !ok {
		return // a request has it: in use, not abandoned
	}
	defer unlock()
	if !k.idle(dir, id) { // a request may have written to it just before
		return
	}
	var info struct {
		Size     int64
		MetaData map[string]string
	}
	if b, err := os.ReadFile(filepath.Join(dir, id+infoSuffix)); err == nil {
		_ = json.Unmarshal(b, &info) // a damaged info still gets removed, just unattributed
	}
	var staged int64
	landed := false // the data is also linked into place: only the staged name is left
	if fi, err := os.Lstat(filepath.Join(dir, id)); err == nil {
		staged = fi.Size()
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			landed = st.Nlink > 1
		}
	}
	ev := audit.Event{Operation: "upload_expire", User: info.MetaData[metaUser], Path: info.MetaData[metaPath], Bytes: staged, Outcome: audit.OK}
	// Info first: once it is gone tusd no longer finds the Upload.
	for _, name := range []string{id + infoSuffix, id} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			k.cfg.Log.Error("expire upload", "mount", m.Name, "id", id, "err", scrub.Path(err))
			ev.Outcome = audit.Failed
		}
	}
	if ev.Outcome == audit.OK && landed {
		return // a finished Upload's leftover staged name: housekeeping, not an expiry
	}
	// A complete Upload that never landed (it waited for a late Conflict to be settled)
	// is an expiry like any other: its data is thrown away.
	if k.cfg.Audit != nil {
		k.cfg.Audit.Record(ev)
	}
}

// Run sweeps once now (Uploads left by an earlier run count) and then every interval
// until ctx is done.
func (k *Keeper) Run(ctx context.Context, interval time.Duration) {
	k.Sweep(ctx)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			k.Sweep(ctx)
		}
	}
}
