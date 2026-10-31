package trash

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
	"github.com/azthrun/dreambig-nas-files/internal/scrub"
)

// purgingSuffix marks a Trash folder that was set aside to be removed. A crash can leave
// one behind; the next purge finishes it.
const purgingSuffix = ".purging"

// Purge removes every dated Trash folder that is older than the retention, in every
// writable Mount. Age is the folder's date, not its mtime (a rename keeps the old mtime).
// A folder dated d is purged once today is more than RetentionDays after d. With
// RetentionDays 0 nothing is ever purged. Each purged folder, and each failure, is one
// audit line (operation "trash_purge"); Purge never panics or returns an error.
//
// A big folder can take minutes to remove, so the write lock is held only to set the
// expired folders aside (a rename); the removal runs without it and deletes are not held
// up. A set-aside folder is no longer a dated one, so nothing is placed into it.
func (t *Trash) Purge() {
	for _, a := range t.setAside() {
		outcome := audit.OK
		if err := t.cfg.RemoveAll(a.root, a.moved); err != nil {
			outcome = audit.Failed
			t.logFailure(a.mount, err)
		}
		t.record(a.mount, a.orig, outcome)
	}
}

// aside is one expired Trash folder that was set aside for removal.
type aside struct {
	mount string
	root  *os.Root
	orig  string // where it was: TrashDir/<date>
	moved string // where it is now: orig + purgingSuffix
}

// setAside renames every expired dated folder to its set-aside name, under the write
// lock, and returns them together with the leftovers of an earlier run.
func (t *Trash) setAside() []aside {
	t.mu.Lock()
	defer t.mu.Unlock()
	today := dateOnly(t.cfg.Clock.Now())
	var out []aside
	for _, m := range t.cfg.Mounts {
		root, ok := t.roots[m.Name]
		if !ok || t.readOnly[m.Name] {
			continue
		}
		f, err := resolver.OpenRead(root, TrashDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.record(m.Name, TrashDir, audit.Failed)
			t.logFailure(m.Name, err)
			continue
		}
		des, err := f.ReadDir(-1)
		_ = f.Close()
		if err != nil {
			t.record(m.Name, TrashDir, audit.Failed)
			t.logFailure(m.Name, err)
			continue
		}
		for _, de := range des {
			if !de.IsDir() { // only dated folders are ours to purge
				continue
			}
			name := de.Name()
			if date, left := strings.CutSuffix(name, purgingSuffix); left {
				if _, err := time.Parse("2006-01-02", date); err == nil {
					orig := path.Join(TrashDir, date)
					out = append(out, aside{m.Name, root, orig, path.Join(TrashDir, name)})
				}
				continue
			}
			d, err := time.Parse("2006-01-02", name)
			if err != nil || t.cfg.RetentionDays <= 0 || int(today.Sub(d).Hours()/24) <= t.cfg.RetentionDays {
				continue
			}
			orig := path.Join(TrashDir, name)
			moved := orig + purgingSuffix
			if err := root.Rename(orig, moved); err != nil {
				t.record(m.Name, orig, audit.Failed)
				t.logFailure(m.Name, err)
				continue
			}
			out = append(out, aside{m.Name, root, orig, moved})
		}
	}
	return out
}

// dateOnly is t's calendar date as midnight UTC, so day counts ignore time zones and DST.
func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// logFailure keeps the cause of a failed purge, which the audit line has no room for.
func (t *Trash) logFailure(mount string, err error) {
	t.cfg.Log.Error("trash purge", "mount", mount, "err", scrub.Path(err))
}

func (t *Trash) record(mount, rel string, outcome audit.Outcome) {
	if t.cfg.Audit == nil {
		return
	}
	t.cfg.Audit.Record(audit.Event{Operation: "trash_purge", Path: mount + "/" + rel, Outcome: outcome})
}

// Run purges once now and then every interval until ctx is done. Call it in its own
// goroutine; it returns when ctx is cancelled.
func (t *Trash) Run(ctx context.Context, interval time.Duration) {
	t.Purge()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.Purge()
		}
	}
}
