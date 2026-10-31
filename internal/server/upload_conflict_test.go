package server_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

// tusCreateChoice starts an Upload of size bytes to path carrying the Conflict choice.
func tusCreateChoice(c *testenv.Client, path, choice string, size int) *httptest.ResponseRecorder {
	return c.Do(tusReq(c, http.MethodPost, "/uploads/", "", map[string]string{
		"Upload-Length":   strconv.Itoa(size),
		"Upload-Metadata": tusMeta(map[string]string{"path": path, "conflict": choice}),
	}))
}

// uploadWith uploads body to path with a Conflict choice in one create and one PATCH and
// returns the PATCH response.
func uploadWith(t *testing.T, c *testenv.Client, path, choice, body string) *httptest.ResponseRecorder {
	t.Helper()
	loc := uploadURL(t, tusCreateChoice(c, path, choice, len(body)))
	return tusPatch(c, loc, 0, body)
}

func lastAuditOf(t *testing.T, e *testenv.Env, op string) map[string]any {
	t.Helper()
	var got map[string]any
	for _, l := range auditLines(t, e) {
		if l["operation"] == op {
			got = l
		}
	}
	return got
}

func TestReplaceMovesTheOldFileToTrashAndPlacesTheNewOne(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/report.pdf", "old")
	c := e.SignIn("alice")

	if rec := uploadWith(t, c, "docs/report.pdf", "replace", "new!"); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}

	if got := e.ReadFile("docs/report.pdf"); got != "new!" {
		t.Fatalf("file = %q", got)
	}
	if got := e.ReadFile("docs/" + trashDay + "/report.pdf"); got != "old" {
		t.Fatalf("Trash holds %q, want the old file", got)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}
	if l := lastAuditOf(t, e, "upload"); l == nil || l["choice"] != "replace" || l["outcome"] != "ok" {
		t.Errorf("completion not audited with the choice: %v", l)
	}
}
