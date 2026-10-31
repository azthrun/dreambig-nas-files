package limiter_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/clock"
	"github.com/azthrun/dreambig-nas-files/internal/limiter"
)

// What the limiter decides for sign-ins is tested at the HTTP seam in
// internal/server/auth_test.go; its memory use cannot be seen through HTTP.

func TestLimiterForgetsExpiredKeysWithoutBeingAskedAboutThem(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	l := limiter.New(clk)
	for i := 0; i < 50; i++ {
		l.Try(fmt.Sprintf("ip:192.0.2.%d", i))
	}
	clk.Advance(limiter.Window + time.Minute)
	l.Try("ip:198.51.100.1") // the next attempt anywhere sweeps the old ones

	if n := l.Len(); n != 1 {
		t.Errorf("limiter still holds %d keys, want 1", n)
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	l := limiter.New(clk)
	for i := 0; i < limiter.MaxKeys*2; i++ {
		l.Try(fmt.Sprintf("ip:%d", i))
		clk.Advance(time.Millisecond) // all inside the window
	}
	if n := l.Len(); n > limiter.MaxKeys {
		t.Errorf("limiter holds %d keys, want at most %d", n, limiter.MaxKeys)
	}
	// The newest failures are the ones kept.
	for i := 0; i < limiter.MaxFailures; i++ {
		l.Try("user:newest")
	}
	if _, ok := l.Try("user:newest"); ok {
		t.Error("a key that just reached the limit is not blocked")
	}
}

func TestForgivenAttemptsLeaveNoTrace(t *testing.T) {
	l := limiter.New(clock.NewFake(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)))
	forgive, ok := l.Try("user:alice")
	if !ok {
		t.Fatal("first attempt refused")
	}
	forgive()
	if n := l.Len(); n != 0 {
		t.Errorf("limiter holds %d keys after a forgiven attempt", n)
	}
}

func TestLimiterKeysOfHugeNamesAreShort(t *testing.T) {
	long := strings.Repeat("x", 100_000)
	if k := limiter.UserKey(long); len(k) > 200 {
		t.Errorf("key for a %d-byte name is %d bytes", len(long), len(k))
	}
}
