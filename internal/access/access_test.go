package access_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
)

var (
	users = []string{"alice", "bob"}
	ms    = []mounts.Mount{{Name: "docs"}, {Name: "archive", ReadOnly: true}}
)

func TestAccessIsPerUserPerMountAndDefaultsToNone(t *testing.T) {
	tab, err := access.Parse(strings.NewReader(`{"alice":{"docs":"write","archive":"write"},"bob":{"docs":"read"}}`), users, ms)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		user  string
		mount mounts.Mount
		want  access.Level
	}{
		{"alice", ms[0], access.Write},
		{"bob", ms[0], access.Read},
		{"bob", ms[1], access.None},    // not listed: none
		{"nobody", ms[0], access.None}, // not a User: none
		{"alice", ms[1], access.Read},  // a read-only Mount caps write at read
	}
	for _, c := range cases {
		if got := tab.Level(c.user, c.mount); got != c.want {
			t.Errorf("Level(%s, %s) = %v, want %v", c.user, c.mount.Name, got, c.want)
		}
	}
}

func TestParseRefusesBadFiles(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":       `{"alice":`,
		"not an object":   `["alice"]`,
		"unknown user":    `{"mallory":{"docs":"read"}}`,
		"unknown mount":   `{"alice":{"photos":"read"}}`,
		"unknown level":   `{"alice":{"docs":"admin"}}`,
		"trailing data":   `{} {}`,
		"bad nesting":     `{"alice":{"docs":{"x":1}}}`,
		"duplicate user":  `{"alice":{"docs":"read"},"alice":{"docs":"write"}}`,
		"duplicate mount": `{"alice":{"docs":"read","docs":"write"}}`,
		"empty":           ``,
	} {
		if _, err := access.Parse(strings.NewReader(content), users, ms); err == nil {
			t.Errorf("%s: Parse accepted %q", name, content)
		}
	}
}

func TestLoadRefusesMissingFile(t *testing.T) {
	if _, err := access.Load(filepath.Join(t.TempDir(), "missing.json"), users, ms); err == nil {
		t.Fatal("missing Access file accepted")
	}
}

func TestEmptyObjectMeansNoAccessAtAll(t *testing.T) {
	tab, err := access.Parse(strings.NewReader(`{}`), users, ms)
	if err != nil {
		t.Fatal(err)
	}
	if tab.Level("alice", ms[0]) != access.None {
		t.Fatal("empty Access file granted access")
	}
}
