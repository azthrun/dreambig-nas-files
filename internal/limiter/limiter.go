// Package limiter is the sign-in rate limit: this many attempts that did not succeed
// within the window lock a User name, and separately a client address, until the oldest
// one ages out.
package limiter

import (
	"slices"
	"sync"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
)

const (
	// MaxFailures is how many failures within Window lock a key.
	MaxFailures = 5
	// Window is how long a failure counts.
	Window = 15 * time.Minute

	// MaxKeys bounds the table's memory: anyone can make up user names and, behind a
	// proxy, client addresses, and the sign-in page needs no session.
	MaxKeys = 10_000
	// maxKeyName is the longest User name kept apart; a longer one is cut.
	maxKeyName = 128
)

// UserKey and IPKey are the limiter keys of one sign-in attempt.
func UserKey(name string) string {
	if len(name) > maxKeyName {
		name = name[:maxKeyName]
	}
	return "user:" + name
}

// IPKey is the key of a client address.
func IPKey(ip string) string { return "ip:" + ip }

// Limiter counts recent failures per key. Keys are swept as they expire and the table
// never holds more than MaxKeys.
type Limiter struct {
	clk       clock.Clock
	mu        sync.Mutex
	m         map[string][]time.Time
	lastSweep time.Time
}

// New returns an empty Limiter.
func New(clk clock.Clock) *Limiter {
	return &Limiter{clk: clk, m: map[string][]time.Time{}}
}

// Len is the number of keys held.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

// recent returns the failures of key still inside the window; callers hold l.mu.
func (l *Limiter) recent(key string) []time.Time {
	cut := l.clk.Now().Add(-Window)
	ts := l.m[key]
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.m, key)
	} else {
		l.m[key] = ts
	}
	return ts
}

// Try claims one attempt against every key, or returns ok false when any key is at the
// limit. The attempt counts as a failure from this moment, in one step with the check, so
// requests arriving together cannot all pass it while the first are still being judged;
// call forgive when it turns out to have succeeded.
func (l *Limiter) Try(keys ...string) (forgive func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if len(l.recent(k)) >= MaxFailures {
			return nil, false
		}
	}
	now := l.clk.Now()
	l.sweep(now)
	for _, k := range keys {
		l.recent(k)
		l.m[k] = append(l.m[k], now)
	}
	return func() { l.forgive(now, keys) }, true
}

// forgive takes back the attempt made at the time at. A key may be gone already.
func (l *Limiter) forgive(at time.Time, keys []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		ts := l.m[k]
		i := slices.Index(ts, at)
		if i < 0 {
			continue
		}
		if len(ts) == 1 {
			delete(l.m, k)
		} else {
			l.m[k] = slices.Delete(slices.Clone(ts), i, i+1)
		}
	}
}

// sweep forgets keys whose failures have all aged out (at most once a minute, or at
// once when the table is full) and, if it is still full, drops the tenth of the keys
// that failed longest ago. Callers hold l.mu.
func (l *Limiter) sweep(now time.Time) {
	full := len(l.m) >= MaxKeys
	if !full && now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for k := range l.m {
		l.recent(k)
	}
	if len(l.m) < MaxKeys {
		return
	}
	type last struct {
		key string
		at  time.Time
	}
	all := make([]last, 0, len(l.m))
	for k, ts := range l.m {
		all = append(all, last{k, ts[len(ts)-1]})
	}
	slices.SortFunc(all, func(a, b last) int { return a.at.Compare(b.at) })
	for _, e := range all[:len(all)/10+1] {
		delete(l.m, e.key)
	}
}
