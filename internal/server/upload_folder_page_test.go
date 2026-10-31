package server_test

import (
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/i18n"
)

// What the browser does with a dropped tree is checked by hand (and with a throwaway
// Node harness); here only what the server renders and serves is asserted.

func TestFolderPageOffersAFolderPickerWhereTheUserCanWrite(t *testing.T) {
	e := upEnv(t)
	body := e.SignIn("alice").Get("/files/docs").Body.String()
	if !strings.Contains(body, `webkitdirectory`) || !strings.Contains(body, `id="upload-folder"`) {
		t.Error("write page has no folder picker")
	}
	if strings.Contains(e.SignIn("alice").Get("/files/archive").Body.String(), "webkitdirectory") {
		t.Error("a read-only Mount offers a folder picker")
	}
}

func TestFolderUploadScriptUsesEntriesAndPagesThroughThem(t *testing.T) {
	e := upEnv(t)
	js := e.Anonymous().Get("/static/upload.js").Body.String()
	for _, want := range []string{"webkitGetAsEntry", "readEntries", "webkitRelativePath", "normalize(\"NFC\")", "MAX_PARALLEL"} {
		if !strings.Contains(js, want) {
			t.Errorf("upload.js lacks %q", want)
		}
	}
}

func TestFolderUploadTextComesFromBothCatalogs(t *testing.T) {
	e := upEnv(t)
	c := e.SignIn("alice")
	for _, lang := range []string{"en", "zh-Hans"} {
		c.AcceptLanguage = lang
		body := c.Get("/files/docs").Body.String()
		for _, key := range []string{"upload.choose_folder", "upload.drop_hint", "upload.summary", "upload.empty_skipped", "upload.reading", "upload.reload_hint"} {
			want := i18n.T(lang, key)
			if want == key || !strings.Contains(body, htmlEscape(want)) {
				t.Errorf("%s: page lacks %q (%s)", lang, want, key)
			}
		}
	}
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}
