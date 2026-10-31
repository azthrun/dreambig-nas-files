package config_test

import (
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/config"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := config.Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.Root != "/data" || !c.RequireMountPoints || c.UsersFile != "/config/users.htpasswd" {
		t.Errorf("defaults wrong: %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		"NAS_FILES_ADDR":                 "127.0.0.1:9000",
		"NAS_FILES_ROOT":                 "/srv",
		"NAS_FILES_REQUIRE_MOUNT_POINTS": "false",
		"NAS_FILES_USERS_FILE":           "/etc/users",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:9000" || c.Root != "/srv" || c.RequireMountPoints || c.UsersFile != "/etc/users" {
		t.Errorf("overrides ignored: %+v", c)
	}
}

func TestTrashRetentionDefaultsTo30DaysAndAcceptsZeroForNever(t *testing.T) {
	c, err := config.Load(env(nil))
	if err != nil || c.TrashRetentionDays != 30 {
		t.Fatalf("default = %d, %v", c.TrashRetentionDays, err)
	}
	c, err = config.Load(env(map[string]string{"NAS_FILES_TRASH_RETENTION_DAYS": "0"}))
	if err != nil || c.TrashRetentionDays != 0 {
		t.Errorf("0 = %d, %v", c.TrashRetentionDays, err)
	}
	c, err = config.Load(env(map[string]string{"NAS_FILES_TRASH_RETENTION_DAYS": "7"}))
	if err != nil || c.TrashRetentionDays != 7 {
		t.Errorf("7 = %d, %v", c.TrashRetentionDays, err)
	}
}

func TestTrashRetentionRejectsNonsense(t *testing.T) {
	for _, v := range []string{"-1", "x", "1.5"} {
		c, err := config.Load(env(map[string]string{"NAS_FILES_TRASH_RETENTION_DAYS": v, "NAS_FILES_ADDR": ":9999"}))
		if err == nil {
			t.Errorf("%q accepted", v)
		}
		if c.Addr != ":9999" {
			t.Errorf("Addr lost for %q", v)
		}
	}
}

func TestLoadRejectsBadBoolButStillReturnsAddr(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		"NAS_FILES_ADDR":                 ":9999",
		"NAS_FILES_REQUIRE_MOUNT_POINTS": "maybe",
	}))
	if err == nil {
		t.Fatal("want an error for a non-boolean")
	}
	if c.Addr != ":9999" {
		t.Errorf("Addr must survive an error so -healthcheck works, got %q", c.Addr)
	}
}
