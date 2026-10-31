package server_test

import (
	"net/http"
	"testing"
)

func TestARefusedUploadRequestIsAuditedWhateverItsMethod(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := uploadURL(t, tusCreate(c, "docs/a.txt", 3))

	for _, r := range [][2]string{
		{http.MethodGet, "/uploads/"},
		{http.MethodGet, loc},
		{http.MethodGet, loc + "/resolve"},
		{http.MethodGet, "/uploads/skip"},
		{http.MethodPut, loc},
		{http.MethodHead, "/uploads/"},
	} {
		before := len(refusalsOf(t, e, "upload_status"))
		rec := c.Do(tusReq(c, r[0], r[1], "", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s = %d", r[0], r[1], rec.Code)
		}
		ls := refusalsOf(t, e, "upload_status")
		if len(ls) != before+1 || ls[before]["user"] != "alice" || ls[before]["status"] != float64(405) {
			t.Fatalf("%s %s: want one refused line, got %v", r[0], r[1], ls)
		}
	}
}

// OPTIONS only asks what the server can do, and a HEAD on an Upload that is gone is how a
// client finds out it must start again. Neither is a User being refused anything.
func TestCapabilityDiscoveryAndRoutineHeadsAreNotAudited(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	before := len(auditLines(t, e))
	c.Do(tusReq(c, http.MethodOptions, "/uploads/", "", nil))
	tusHead(c, "/uploads/00000000000000000000000000000000")
	if ls := auditLines(t, e); len(ls) != before {
		t.Fatalf("audited %v", ls[before:])
	}
}

func TestAnUnknownLanguageIsAudited(t *testing.T) {
	e := upEnv(t)
	if rec := e.Anonymous().Get("/lang/zz"); rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous = %d", rec.Code)
	}
	c := e.SignIn("alice")
	if rec := c.Get("/lang/zz"); rec.Code != http.StatusNotFound {
		t.Fatalf("signed in = %d", rec.Code)
	}
	ls := refusalsOf(t, e, "lang")
	if len(ls) != 2 || ls[0]["status"] != float64(404) || ls[1]["user"] != "alice" {
		t.Fatalf("want a refused line for each, got %v", ls)
	}
}
