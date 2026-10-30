package server_test

import (
	"strings"
	"testing"
)

// The selection works with JS off: a plain form, checkboxes, two submit actions' worth
// of plumbing (action=zip here), and the CSRF token.
func TestBrowseOffersSelectionAndZipToReadOnlyUsers(t *testing.T) {
	e := zipEnv(t)
	c := e.SignIn("bob") // read Access
	body := c.Get("/files/docs/proj").Body.String()
	for _, want := range []string{
		`<form method="post" action="/files/docs/proj"`,
		`name="csrf_token"`,
		`type="checkbox" name="names" value="a.txt"`,
		`type="checkbox" name="names" value="sub"`,
		`name="op" value="zip"`,
		`href="/files/docs/proj?zip=1"`,
		"Download selected as zip",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	c.AcceptLanguage = "zh-CN"
	if !strings.Contains(c.Get("/files/docs/proj").Body.String(), "下载所选为 zip") {
		t.Error("Chinese label missing")
	}
}
