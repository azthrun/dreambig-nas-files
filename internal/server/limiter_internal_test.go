package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
)

// This one test lives inside the package: the sign-in limiter's memory use cannot be seen
// through HTTP. Everything about what the limiter decides is tested at the HTTP seam in
// auth_test.go.

// size is the number of keys held.
func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

func TestLimiterForgetsExpiredKeysWithoutBeingAskedAboutThem(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	l := newLimiter(clk)
	for i := 0; i < 50; i++ {
		l.fail(fmt.Sprintf("ip:192.0.2.%d", i))
	}
	clk.Advance(failureWindow + time.Minute)
	l.fail("ip:198.51.100.1") // the next failure anywhere sweeps the old ones

	if n := l.size(); n != 1 {
		t.Errorf("limiter still holds %d keys, want 1", n)
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	l := newLimiter(clk)
	for i := 0; i < maxLimiterKeys*2; i++ {
		l.fail(fmt.Sprintf("ip:%d", i))
		clk.Advance(time.Millisecond) // all inside the window
	}
	if n := l.size(); n > maxLimiterKeys {
		t.Errorf("limiter holds %d keys, want at most %d", n, maxLimiterKeys)
	}
	// The newest failures are the ones kept.
	for i := 0; i < maxFailures; i++ {
		l.fail("user:newest")
	}
	if !l.blocked("user:newest") {
		t.Error("a key that just reached the limit is not blocked")
	}
}

func TestLimiterKeysOfHugeNamesAreShort(t *testing.T) {
	long := strings.Repeat("x", 100_000)
	if k := userKey(long); len(k) > 200 {
		t.Errorf("key for a %d-byte name is %d bytes", len(long), len(k))
	}
}
