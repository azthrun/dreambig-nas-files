package server_test

import (
	"net/http"
	"testing"
)

// The audit line says where the file really is, not where the User asked it to go.
func TestKeepBothAuditsTheFinalPathAndRemembersTheRequestedOne(t *testing.T) {
	e := upEnv(t)
	e.WriteFile("docs/report.pdf", "old")
	e.WriteFile("docs/report (1).pdf", "older")
	c := e.SignIn("alice")

	if rec := uploadWith(t, c, "docs/report.pdf", "keep", "new"); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}
	l := lastAuditOf(t, e, "upload")
	if l == nil || l["path"] != "docs/report (2).pdf" || l["requested"] != "docs/report.pdf" || l["choice"] != "keep" {
		t.Fatalf("want the final Path and the requested one, got %v", l)
	}
	if got := e.ReadFile("docs/report (2).pdf"); got != "new" {
		t.Errorf("file = %q", got)
	}
}

// Keep both lands beside a link's target, so that is the Path the line names.
func TestKeepBothOnASymlinkAuditsWhereTheFileIs(t *testing.T) {
	for name, tc := range map[string]struct{ target, final string }{
		"same Mount":    {"store/r.pdf", "docs/store/r (1).pdf"},
		"another Mount": {"../other/r.pdf", "other/r (1).pdf"},
	} {
		t.Run(name, func(t *testing.T) {
			e := upEnv(t)
			e.WriteFile("docs/store/r.pdf", "old")
			e.WriteFile("other/r.pdf", "old")
			e.Symlink("docs/x.pdf", tc.target)
			c := e.SignIn("alice")

			if rec := uploadWith(t, c, "docs/x.pdf", "keep", "new"); rec.Code != http.StatusNoContent {
				t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
			}
			if got := e.ReadFile(tc.final); got != "new" {
				t.Fatalf("file = %q", got)
			}
			l := lastAuditOf(t, e, "upload")
			if l == nil || l["path"] != tc.final || l["requested"] != "docs/x.pdf" {
				t.Fatalf("want %s and the requested Path, got %v", tc.final, l)
			}
		})
	}
}

func TestALateKeepBothAuditsTheFinalPath(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	loc := lateConflict(t, e, c)
	if rec := resolve(c, loc, "keep"); rec.Code != http.StatusNoContent {
		t.Fatalf("resolve = %d", rec.Code)
	}
	l := lastAuditOf(t, e, "upload")
	if l == nil || l["path"] != "docs/late (1).txt" || l["requested"] != "docs/late.txt" {
		t.Fatalf("want the final Path and the requested one, got %v", l)
	}
}

func TestAnUploadThatLandsWhereAskedHasNoRequestedField(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	if rec := uploadWith(t, c, "docs/plain.txt", "", "abc"); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d", rec.Code)
	}
	l := lastAuditOf(t, e, "upload")
	if l == nil || l["path"] != "docs/plain.txt" || l["requested"] != nil {
		t.Fatalf("got %v", l)
	}
}
