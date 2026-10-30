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
var EnvVars = []string{envAddr, envRoot, envRequireMountPoints, envUsersFile, envAccessFile, envTrashRetentionDays}

const (
	envAddr               = "NAS_FILES_ADDR"
	envRoot               = "NAS_FILES_ROOT"
	envRequireMountPoints = "NAS_FILES_REQUIRE_MOUNT_POINTS"
	envUsersFile          = "NAS_FILES_USERS_FILE"
	envAccessFile         = "NAS_FILES_ACCESS_FILE"
	envTrashRetentionDays = "NAS_FILES_TRASH_RETENTION_DAYS"

	defaultAddr       = ":8080"
	defaultRoot       = "/data"
	defaultUsersFile  = "/config/users.htpasswd"
	defaultAccessFile = "/config/access.json"
	defaultTrashDays  = 30
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
	// AccessFile is the OS path of the read-only JSON file that sets each User's Access
	// per Mount. Startup fails if it is missing or malformed, or names an unknown User
	// or Mount.
	AccessFile string
	// TrashRetentionDays is how long Trash is kept before it is purged; 0 means never.
	TrashRetentionDays int
}

// Load builds a Config from getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:               cmp.Or(getenv(envAddr), defaultAddr),
		Root:               cmp.Or(getenv(envRoot), defaultRoot),
		RequireMountPoints: true,
		UsersFile:          cmp.Or(getenv(envUsersFile), defaultUsersFile),
		AccessFile:         cmp.Or(getenv(envAccessFile), defaultAccessFile),
		TrashRetentionDays: defaultTrashDays,
	}
	if s := getenv(envTrashRetentionDays); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return c, fmt.Errorf("%s: want a whole number of days, 0 or more (0 keeps Trash forever), got %q", envTrashRetentionDays, s)
		}
		c.TrashRetentionDays = n
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
