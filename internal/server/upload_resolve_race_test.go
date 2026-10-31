package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// Two clicks (or two tabs) settling the same late Conflict at once must place the file
// once. Without a lock on the Upload both would link it, "r1 (1).txt" and "r1 (2).txt".
func TestConcurrentResolvesPlaceTheFileOnce(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	var tabs []*testenv.Client
	for i := 0; i < 8; i++ { // a Client is not safe to share between goroutines
		tabs = append(tabs, e.SignIn("alice"))
	}

	for round := 0; round < 150; round++ {
		name := fmt.Sprintf("r%d", round)
		loc := uploadURL(t, tusCreate(c, "docs/"+name+".txt", 6))
		tusPatch(c, loc, 0, "new")
		e.WriteFile("docs/"+name+".txt", "someone else")
		if rec := tusPatch(c, loc, 3, "new"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ERR_CONFLICT") {
			t.Fatalf("last PATCH = %d %q", rec.Code, rec.Body)
		}

		var wg sync.WaitGroup
		codes := make([]int, len(tabs))
		for i := range tabs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = resolve(tabs[i], loc, "keep").Code
			}()
		}
		wg.Wait()

		ok := 0
		for _, code := range codes {
			if code == http.StatusNoContent {
				ok++
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d resolves succeeded (%v), want exactly one", round, ok, codes)
		}
		if exists(e, "docs/"+name+" (2).txt") {
			t.Fatalf("round %d: the file was placed twice", round)
		}
	}
}
