package server

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
)

// sessionLifetime is the sliding idle expiry: every request renews it.
const sessionLifetime = 30 * 24 * time.Hour

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
func (s *sessions) create(user string) string {
	now := s.clk.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.m { // sweep the idle ones
		if !now.Before(v.expires) {
			delete(s.m, k)
		}
	}
	tok := randomToken()
	s.m[tok] = &session{User: user, CSRF: randomToken(), expires: now.Add(sessionLifetime)}
	return tok
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
