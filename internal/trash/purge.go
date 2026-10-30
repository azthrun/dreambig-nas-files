package trash

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
)

// Purge removes every dated Trash folder that is older than the retention, in every
// writable Mount. Age is the folder's date, not its mtime (a rename keeps the old mtime).
// A folder dated d is purged once today is more than RetentionDays after d. With
// RetentionDays 0 nothing is ever purged. Each purged folder, and each failure, is one
// audit line (operation "trash_purge"); Purge never panics or returns an error.
func (t *Trash) Purge() {
	if t.cfg.RetentionDays <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.cfg.Clock.Now()
	today := dateOnly(now)
	for _, m := range t.cfg.Mounts {
		root, ok := t.roots[m.Name]
		if !ok || t.readOnly[m.Name] {
			continue
		}
		f, err := root.Open(TrashDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.record(m.Name, TrashDir, audit.Failed)
			continue
		}
		des, err := f.ReadDir(-1)
		_ = f.Close()
		if err != nil {
			t.record(m.Name, TrashDir, audit.Failed)
			continue
		}
		for _, de := range des {
			d, err := time.Parse("2006-01-02", de.Name())
			if err != nil || !de.IsDir() { // only dated folders are ours to purge
				continue
			}
			if int(today.Sub(d).Hours()/24) <= t.cfg.RetentionDays {
				continue
			}
			rel := path.Join(TrashDir, de.Name())
			outcome := audit.OK
			if err := root.RemoveAll(rel); err != nil {
				outcome = audit.Failed
			}
			t.record(m.Name, rel, outcome)
		}
	}
}

// dateOnly is t's calendar date as midnight UTC, so day counts ignore time zones and DST.
func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
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
