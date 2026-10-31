package audit_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/audit"
	"github.com/azthrun/dreambig-nas-files/internal/clock"
)

func TestRecordEmitsOneJSONLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	clk := clock.NewFake(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	l := audit.New(&buf, clk)

	l.Record(audit.Event{User: "alice", Operation: "signin", Outcome: audit.OK, Status: 303})
	l.Record(audit.Event{User: "alice", Operation: "delete", Path: "docs/a.txt", Outcome: audit.Refused, Status: 403, Bytes: 5})

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"time": "2026-01-15T12:00:00Z", "user": "alice", "operation": "delete",
		"path": "docs/a.txt", "outcome": "refused", "status": float64(403), "bytes": float64(5),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestRecordKeepsMultilineValuesOnOneLine(t *testing.T) {
	var buf bytes.Buffer
	l := audit.New(&buf, clock.NewFake(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)))
	l.Record(audit.Event{User: "al\nice", Operation: "signin", Outcome: audit.Refused})
	if strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("event spans several lines: %q", buf.String())
	}
}
