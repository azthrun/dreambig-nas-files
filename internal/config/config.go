// Package config reads the service's NAS_FILES_* settings. No other package reads
// the environment.
package config

import (
	"cmp"
	"fmt"
	"strconv"
)

// EnvVars lists every environment variable the service itself reads. Variables used
// only by compose.yaml (UID/GID, host port) are not here; the env-docs test reads those
// from compose.yaml. Add every new variable here AND to .env.example.
var EnvVars = []string{envAddr, envRoot, envRequireMountPoints, envUsersFile}

const (
	envAddr               = "NAS_FILES_ADDR"
	envRoot               = "NAS_FILES_ROOT"
	envRequireMountPoints = "NAS_FILES_REQUIRE_MOUNT_POINTS"
	envUsersFile          = "NAS_FILES_USERS_FILE"

	defaultAddr      = ":8080"
	defaultRoot      = "/data"
	defaultUsersFile = "/config/users.htpasswd"
)

// Config is the service's runtime configuration.
type Config struct {
	// Addr is the host:port the HTTP server listens on. Load sets it even when it
	// returns an error, so the healthcheck probe can run without the rest.
	Addr string
	// Root is the OS path of the data directory whose sub-folders are the Mounts.
	Root string
	// RequireMountPoints makes startup refuse a Mount that is not its own mount point.
	// Only turn it off for local runs and tests, where folders share a filesystem.
	RequireMountPoints bool
	// UsersFile is the OS path of the read-only htpasswd-format file of Users with
	// bcrypt hashes (ADR-0004). Startup fails if it is missing or malformed.
	UsersFile string
}

// Load builds a Config from getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:               cmp.Or(getenv(envAddr), defaultAddr),
		Root:               cmp.Or(getenv(envRoot), defaultRoot),
		RequireMountPoints: true,
		UsersFile:          cmp.Or(getenv(envUsersFile), defaultUsersFile),
	}
	if s := getenv(envRequireMountPoints); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return c, fmt.Errorf("%s: want true or false, got %q", envRequireMountPoints, s)
		}
		c.RequireMountPoints = b
	}
	return c, nil
}
