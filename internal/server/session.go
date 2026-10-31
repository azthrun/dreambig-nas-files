package server

import (
	"crypto/rand"
	"encoding/base64"
	"slices"
	"sync"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
)

const (
	// sessionLifetime is the sliding idle expiry: every request renews it.
	sessionLifetime = 30 * 24 * time.Hour

	// Sign-in rate limit: this many failures within the window lock the User name, and
	// separately the client address, until the oldest failure ages out.
	maxFailures   = 5
	failureWindow = 15 * time.Minute

	// maxLimiterKeys bounds the limiter's memory: anyone can make up user names and, behind
	// a proxy, client addresses, and the sign-in page needs no session.
	maxLimiterKeys = 10_000
	// maxKeyName is the longest User name the limiter keeps apart; a longer one is cut.
	maxKeyName = 128
)

// session is one signed-in browser. Sessions live in memory: a restart signs everyone
// out, which is acceptable for a home service and avoids a database (ADR-0004).
type session struct {
	User    string
	CSRF    string
	expires time.Time
}

type sessions struct {
	clk clock.Clock
	mu  sync.Mutex
	m   map[string]*session
}

func newSessions(clk clock.Clock) *sessions {
	return &sessions{clk: clk, m: map[string]*session{}}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// create starts a session for user and returns its cookie token.
func (s *sessions) create(user string) (string, *session) {
	now := s.clk.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.m { // sweep the idle ones
		if !now.Before(v.expires) {
			delete(s.m, k)
		}
	}
	tok := randomToken()
	sess := &session{User: user, CSRF: randomToken(), expires: now.Add(sessionLifetime)}
	s.m[tok] = sess
	return tok, sess
}

// touch returns the live session for token and renews its expiry, or nil.
func (s *sessions) touch(token string) *session {
	now := s.clk.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return nil
	}
	if !now.Before(sess.expires) {
		delete(s.m, token)
		return nil
	}
	sess.expires = now.Add(sessionLifetime)
	return sess
}

func (s *sessions) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

// userKey and ipKey are the limiter keys of one sign-in attempt.
func userKey(name string) string {
	if len(name) > maxKeyName {
		name = name[:maxKeyName]
	}
	return "user:" + name
}

func ipKey(ip string) string { return "ip:" + ip }

// limiter counts recent sign-in failures per key. Keys are swept as they expire and the
// table never holds more than maxLimiterKeys.
type limiter struct {
	clk       clock.Clock
	mu        sync.Mutex
	m         map[string][]time.Time
	lastSweep time.Time
}

func newLimiter(clk clock.Clock) *limiter {
	return &limiter{clk: clk, m: map[string][]time.Time{}}
}

// recent returns the failures of key still inside the window; callers hold l.mu.
func (l *limiter) recent(key string) []time.Time {
	cut := l.clk.Now().Add(-failureWindow)
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

// blocked reports whether any key has reached the failure limit.
func (l *limiter) blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if len(l.recent(k)) >= maxFailures {
			return true
		}
	}
	return false
}

// fail records one failure against every key.
func (l *limiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	l.sweep(now)
	for _, k := range keys {
		l.recent(k)
		l.m[k] = append(l.m[k], now)
	}
}

// sweep forgets keys whose failures have all aged out (at most once a minute, or at
// once when the table is full) and, if it is still full, drops the tenth of the keys
// that failed longest ago. Callers hold l.mu.
func (l *limiter) sweep(now time.Time) {
	full := len(l.m) >= maxLimiterKeys
	if !full && now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for k := range l.m {
		l.recent(k)
	}
	if len(l.m) < maxLimiterKeys {
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
