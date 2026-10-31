package server_test

import (
	"net/http"
	"testing"
	"time"
)

// A late Conflict waits for the User's choice with the data kept. If the service
// restarts meanwhile, the Upload must still not look complete: tus-js-client would report
// "done" for a file that never landed.
func TestALateConflictStillWaitsForTheUserAfterARestart(t *testing.T) {
	e := upEnv(t)
	loc := lateConflict(t, e, e.SignIn("alice"))

	e.MustRestart()
	c := e.SignIn("alice")

	if rec := tusHead(c, loc); rec.Code != http.StatusNotFound {
		t.Fatalf("HEAD of an Upload waiting for a choice = %d (Upload-Offset %q), must not look complete",
			rec.Code, rec.Header().Get("Upload-Offset"))
	}
	if rec := resolve(c, loc, "keep"); rec.Code != http.StatusNoContent {
		t.Fatalf("resolve after the restart = %d: %s", rec.Code, rec.Body)
	}
	if got := e.ReadFile("docs/late (1).txt"); got != "newnew" {
		t.Errorf("kept file = %q, want the uploaded data", got)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("staged data left behind")
	}
}

func TestACompleteUploadNobodyResolvedIsAuditedWhenItExpires(t *testing.T) {
	e := upEnv(t)
	lateConflict(t, e, e.SignIn("alice"))

	e.Clock.Advance(8 * 24 * time.Hour)
	e.ExpireUploads()

	l := lastAuditOf(t, e, "upload_expire")
	if l == nil || l["user"] != "alice" || l["path"] != "docs/late.txt" || l["outcome"] != "ok" || l["bytes"] != float64(6) {
		t.Fatalf("expiry of a staged-complete Upload not audited: %v", l)
	}
	if !stagingEmpty(e, "docs") {
		t.Error("expired data left behind")
	}
}
