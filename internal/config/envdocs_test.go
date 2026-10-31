package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/config"
)

var envVarRe = regexp.MustCompile(`NAS_FILES_[A-Z0-9_]+`)

func varsIn(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return envVarRe.FindAllString(string(b), -1)
}

// Every NAS_FILES_* variable the service or compose.yaml uses is documented in
// .env.example, and .env.example documents nothing else.
func TestEnvExampleDocumentsExactlyTheVariablesInUse(t *testing.T) {
	documented := varsIn(t, ".env.example")
	used := append(slices.Clone(config.EnvVars), varsIn(t, "compose.yaml")...)

	for _, v := range used {
		if !slices.Contains(documented, v) {
			t.Errorf("%s is used but not documented in .env.example", v)
		}
	}
	for _, v := range documented {
		if !slices.Contains(used, v) {
			t.Errorf("%s is documented in .env.example but used neither by the service nor compose.yaml", v)
		}
	}
}

// The README's table lists every NAS_FILES_* variable the service or compose.yaml uses.
func TestReadmeListsEveryVariable(t *testing.T) {
	readme := varsIn(t, "README.md")
	used := append(slices.Clone(config.EnvVars), varsIn(t, "compose.yaml")...)
	for _, v := range used {
		if !slices.Contains(readme, v) {
			t.Errorf("%s is used but not mentioned in README.md", v)
		}
	}
	for _, v := range readme {
		if !slices.Contains(used, v) {
			t.Errorf("README.md mentions %s, which nothing uses", v)
		}
	}
}

// compose.yaml pins a release tag the owner cuts, never :latest or a placeholder.
func TestComposePinsAReleaseTag(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*image:\s*ghcr\.io/azthrun/dreambig-nas-files:(\S+)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("compose.yaml has no pinned dreambig-nas-files image")
	}
	if got := string(m[1]); got != "v1.0.0" {
		t.Errorf("image tag = %q, want v1.0.0", got)
	}
}
