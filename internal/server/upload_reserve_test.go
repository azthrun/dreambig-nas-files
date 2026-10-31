package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// Free space only shrinks as an Upload's bytes arrive, so Uploads that were admitted but
// are still on their way must count against the reserve, or ten Uploads that each fit
// would fill the disk together.
func TestUploadsOnTheirWayCountAgainstTheReserve(t *testing.T) {
	free := map[string]int64{"docs": 1000, "media": 1000}
	e := testenv.New(t, testenv.Mount("docs"), testenv.Mount("media"),
		testenv.FreeSpaceReserve(100),
		testenv.FreeSpace(func(mount string) int64 { return free[mount] }))
	c := e.SignIn(testenv.DefaultUser)

	a := uploadURL(t, tusCreate(c, "docs/a.bin", 600)) // 1000 - 100 reserve = 900 left to give
	uploadURL(t, tusCreate(c, "docs/b.bin", 300))      // 900 - 600 = 300: fits exactly

	if rec := tusCreate(c, "docs/c.bin", 1); rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("a third Upload with every byte already promised = %d, want 507", rec.Code)
	}
	// Another Mount is another filesystem.
	uploadURL(t, tusCreate(c, "media/a.bin", 600))

	// Bytes that arrived are no longer owed: a.bin has 100 left to send.
	if rec := tusPatch(c, a, 0, strings.Repeat("x", 500)); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d", rec.Code)
	}
	uploadURL(t, tusCreate(c, "docs/c.bin", 500)) // 900 - (100 + 300) = 500
	if rec := tusCreate(c, "docs/d.bin", 1); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("one byte too many = %d, want 507", rec.Code)
	}

	// A finished Upload owes nothing.
	if rec := tusPatch(c, a, 500, strings.Repeat("x", 100)); rec.Code != http.StatusNoContent {
		t.Fatalf("final PATCH = %d", rec.Code)
	}
	uploadURL(t, tusCreate(c, "docs/e.bin", 100))
}
