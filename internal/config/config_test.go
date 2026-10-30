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
