// Package config reads the service's NAS_FILES_* settings. No other package reads
// the environment.
package config

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
)

// EnvVars lists every environment variable the service itself reads. Variables used
// only by compose.yaml (UID/GID, host port) are not here; the env-docs test reads those
// from compose.yaml. Add every new variable here AND to .env.example.
var EnvVars = []string{envAddr, envRoot, envRequireMountPoints, envUsersFile, envAccessFile, envTrashRetentionDays,
	envUploadExpiryDays, envFreeSpaceReserveGB}

const (
	envAddr               = "NAS_FILES_ADDR"
	envRoot               = "NAS_FILES_ROOT"
	envRequireMountPoints = "NAS_FILES_REQUIRE_MOUNT_POINTS"
	envUsersFile          = "NAS_FILES_USERS_FILE"
	envAccessFile         = "NAS_FILES_ACCESS_FILE"
	envTrashRetentionDays = "NAS_FILES_TRASH_RETENTION_DAYS"
	envUploadExpiryDays   = "NAS_FILES_UPLOAD_EXPIRY_DAYS"
	envFreeSpaceReserveGB = "NAS_FILES_FREE_SPACE_RESERVE_GB"

	// defaultUploadExpiryDays: a week covers a long weekend away; staged data of a dead
	// Upload is then reclaimed.
	defaultUploadExpiryDays = 7
	// defaultReserveGB (decimal GB): keeps the volume usable for DSM, snapshots and
	// logs. Small next to a DS220+'s disks, big enough to stop a runaway Upload.
	defaultReserveGB = 5.0

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
	// DataDir is the OS path of the data directory whose sub-folders are the Mounts.
	DataDir string
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
	// UploadExpiryDays is how long an incomplete Upload may sit idle before it is
	// removed; 0 keeps them forever.
	UploadExpiryDays int
	// FreeSpaceReserveBytes is the free space a Mount's filesystem must keep after a new
	// Upload's declared length; a new Upload that would break it is refused. 0 disables.
	FreeSpaceReserveBytes int64
}

// Load builds a Config from getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:               cmp.Or(getenv(envAddr), defaultAddr),
		DataDir:            cmp.Or(getenv(envRoot), defaultRoot),
		RequireMountPoints: true,
		UsersFile:          cmp.Or(getenv(envUsersFile), defaultUsersFile),
		AccessFile:         cmp.Or(getenv(envAccessFile), defaultAccessFile),
		TrashRetentionDays: defaultTrashDays,

		UploadExpiryDays:      defaultUploadExpiryDays,
		FreeSpaceReserveBytes: int64(defaultReserveGB * 1e9),
	}
	if s := getenv(envUploadExpiryDays); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return c, fmt.Errorf("%s: want a whole number of days, 0 or more (0 keeps abandoned Uploads forever), got %q", envUploadExpiryDays, s)
		}
		c.UploadExpiryDays = n
	}
	if s := getenv(envFreeSpaceReserveGB); s != "" {
		gb, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(gb) || math.IsInf(gb, 0) || gb < 0 || gb > 1e6 {
			return c, fmt.Errorf("%s: want a number of GB, 0 or more (0 turns the reserve off), got %q", envFreeSpaceReserveGB, s)
		}
		c.FreeSpaceReserveBytes = int64(gb * 1e9)
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
