package server_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
)

// Browser behaviour (progress, resume, cancel in a real browser) is checked by hand; here
// only what the server renders and serves is asserted.

func TestFolderPageOffersUploadOnlyWhereTheUserCanWrite(t *testing.T) {
	e := upEnv(t)
	alice := e.SignIn("alice")
	body := alice.Get("/files/docs/sub").Body.String()
	for _, want := range []string{`id="upload"`, `data-endpoint="/uploads/"`, `data-dest="docs/sub"`, `type="file"`, "multiple"} {
		if !strings.Contains(body, want) {
			t.Errorf("write page lacks %q", want)
		}
	}
	bob := e.SignIn("bob")
	for _, p := range []string{"/files/archive", "/files/docs"} {
		got := bob.Get(p).Body.String()
		if p == "/files/archive" && strings.Contains(got, `id="upload"`) {
			t.Errorf("read-only page %s offers Upload", p)
		}
	}
	if strings.Contains(alice.Get("/files/archive").Body.String(), `id="upload"`) {
		t.Error("a read-only Mount offers Upload")
	}
}

func TestUploadScriptsAreSelfHostedWithTheirLicence(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	body := c.Get("/files/docs").Body.String()
	srcs := regexp.MustCompile(`<script[^>]*\ssrc="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(srcs) == 0 {
		t.Fatal("page loads no script")
	}
	for _, m := range srcs {
		if !strings.HasPrefix(m[1], "/static/") {
			t.Errorf("script from outside the service: %s", m[1])
		}
		if rec := e.Anonymous().Get(m[1]); rec.Code != http.StatusOK {
			t.Errorf("%s = %d", m[1], rec.Code)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Error("inline script on the page")
	}
	lic := e.Anonymous().Get("/static/vendor/tus-js-client/LICENSE")
	if lic.Code != http.StatusOK || !strings.Contains(lic.Body.String(), "MIT") {
		t.Errorf("vendored licence missing: %d", lic.Code)
	}
}

func TestUploadScriptNeedsNoSecureContextAPI(t *testing.T) {
	e := upEnv(t)
	for _, f := range []string{"/static/upload.js", "/static/vendor/tus-js-client/tus.min.js"} {
		body := e.Anonymous().Get(f).Body.String()
		if body == "" {
			t.Fatalf("%s is empty", f)
		}
		for _, banned := range []string{"crypto.subtle", "serviceWorker", "showOpenFilePicker", "showDirectoryPicker"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s uses %s, which needs a secure context", f, banned)
			}
		}
	}
}

func TestUploadPanelTextComesFromBothCatalogs(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	for _, lang := range []string{"en", "zh-Hans"} {
		c.AcceptLanguage = lang
		body := c.Get("/files/docs").Body.String()
		for _, key := range []string{"upload.title", "upload.start", "upload.cancel", "upload.exists", "upload.done"} {
			want := i18n.T(lang, key)
			if want == key || !strings.Contains(body, want) {
				t.Errorf("%s: page lacks %q (%s)", lang, want, key)
			}
		}
	}
}
