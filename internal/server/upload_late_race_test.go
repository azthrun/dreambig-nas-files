package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// Two clients upload the same new Path into a new folder. The race is in how the folder is
// made: the one that made it can find the other's file in it. That is a late Conflict like
// any other, so the loser's data stays for the User to settle.
func TestLosingTheRaceForANewFolderIsALateConflict(t *testing.T) {
	e := upEnv(t)
	var tabs []*testenv.Client
	for i := 0; i < 4; i++ { // a Client is not safe to share between goroutines
		tabs = append(tabs, e.SignIn("alice"))
	}

	for round := 0; round < 400; round++ {
		path := fmt.Sprintf("docs/new%d/f.txt", round)
		locs := make([]string, len(tabs))
		for i, c := range tabs {
			locs[i] = uploadURL(t, tusCreate(c, path, 3))
		}
		var wg sync.WaitGroup
		codes := make([]int, len(tabs))
		bodies := make([]string, len(tabs))
		for i := range tabs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec := tusPatch(tabs[i], locs[i], 0, "abc")
				codes[i], bodies[i] = rec.Code, rec.Body.String()
			}()
		}
		wg.Wait()

		ok := 0
		for i, code := range codes {
			switch {
			case code == http.StatusNoContent:
				ok++
			case code != http.StatusConflict || !strings.Contains(bodies[i], "ERR_CONFLICT"):
				t.Fatalf("round %d: PATCH = %d %q, want a late Conflict", round, code, bodies[i])
			default: // the data waits for the User
				if rec := resolve(tabs[i], locs[i], "skip"); rec.Code != http.StatusNoContent {
					t.Fatalf("round %d: resolve = %d, the data was not kept", round, rec.Code)
				}
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d Uploads landed (%v), want one", round, ok, codes)
		}
	}
}
