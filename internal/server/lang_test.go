package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/testenv"
)

func TestSignInPageFollowsAcceptLanguage(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))

	c := e.Anonymous()
	body := c.Get("/login").Body.String()
	if !strings.Contains(body, `lang="en"`) || !strings.Contains(body, "Password") {
		t.Errorf("default is not English:\n%s", body)
	}

	c.AcceptLanguage = "zh-CN,zh;q=0.9,en;q=0.8"
	body = c.Get("/login").Body.String()
	if !strings.Contains(body, `lang="zh-Hans"`) || !strings.Contains(body, "密码") {
		t.Errorf("Accept-Language zh not honoured:\n%s", body)
	}
	if strings.Contains(body, "Password") {
		t.Errorf("English text left on Chinese page:\n%s", body)
	}
}

func TestSwitchLinkWorksWithoutSessionAndIsRemembered(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.Anonymous() // no session, no JavaScript, no CSRF token
	c.AcceptLanguage = "en"

	rec := c.Get("/lang/zh-Hans?next=/login")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("switch = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	var ck *http.Cookie
	for _, x := range rec.Result().Cookies() {
		if x.Name == "nas_files_lang" {
			ck = x
		}
	}
	if ck == nil || ck.Value != "zh-Hans" {
		t.Fatalf("no language cookie: %v", rec.Result().Cookies())
	}
	if ck.Path != "/" || ck.SameSite != http.SameSiteLaxMode || ck.MaxAge < 30*24*3600 || ck.Secure {
		t.Errorf("cookie attributes wrong: %+v", ck)
	}

	// The saved cookie beats Accept-Language: English header, Chinese page.
	if body := c.Get("/login").Body.String(); !strings.Contains(body, "密码") {
		t.Errorf("choice not remembered:\n%s", body)
	}
	// And back again.
	c.AcceptLanguage = "zh-CN"
	c.Get("/lang/en?next=/login")
	if body := c.Get("/login").Body.String(); !strings.Contains(body, "Password") {
		t.Errorf("switch back to English not remembered:\n%s", body)
	}
}

func TestSwitchIsAPlainLinkOnEveryPageKeepingTheCurrentAddress(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.Mkdir("docs/sub")

	login := e.Anonymous().Get("/login").Body.String()
	for _, want := range []string{`href="/lang/zh-Hans?next=%2Flogin"`, "中文", "English"} {
		if !strings.Contains(login, want) {
			t.Errorf("sign-in page lacks %q:\n%s", want, login)
		}
	}

	c := e.SignIn(testenv.DefaultUser)
	body := c.Get("/files/docs/sub?sort=size&order=desc").Body.String()
	if !strings.Contains(body, `href="/lang/zh-Hans?next=%2Ffiles%2Fdocs%2Fsub%3Fsort%3Dsize%26order%3Ddesc"`) {
		t.Errorf("browse page switch does not return to the same page:\n%s", body)
	}
}

func TestSwitchRefusesOpenRedirectsAndUnknownLanguages(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	for _, next := range []string{"//evil.example", "https://evil.example/", `/\evil.example`, "evil", ""} {
		rec := e.Anonymous().Get("/lang/zh-Hans?next=" + next)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
			t.Errorf("next=%q: %d to %q, want 303 to /", next, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := e.Anonymous().Get("/lang/klingon?next=/login")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown language = %d, want 404", rec.Code)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "nas_files_lang" {
			t.Error("unknown language set a cookie")
		}
	}
}

func TestSwitchOnlyTakesGet(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	rec := e.Do(httptest.NewRequest(http.MethodPost, "/lang/zh-Hans", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestSignedInPagesAndErrorsAreTranslated(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	e.WriteFile("docs/alpha.txt", "12345")
	e.Chtimes("docs/alpha.txt", time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC))
	c := e.SignIn(testenv.DefaultUser)
	c.Get("/lang/zh-Hans")

	body := c.Get("/files/docs").Body.String()
	for _, want := range []string{"2025年3月4日 05:06", "5 字节", "名称", "文件"} {
		if !strings.Contains(body, want) {
			t.Errorf("Chinese browse page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Name") || strings.Contains(body, "2025-03-04 05:06") {
		t.Errorf("English left on Chinese browse page:\n%s", body)
	}
	if root := c.Get("/").Body.String(); !strings.Contains(root, "当前用户") || !strings.Contains(root, "退出登录") {
		t.Errorf("Root page not translated:\n%s", root)
	}
	if nf := c.Get("/files/nope").Body.String(); !strings.Contains(nf, "未找到") {
		t.Errorf("error page not translated:\n%s", nf)
	}

	c.Get("/lang/en")
	if body := c.Get("/files/docs").Body.String(); !strings.Contains(body, "2025-03-04 05:06") || !strings.Contains(body, "5 B") {
		t.Errorf("English formats missing after switching back:\n%s", body)
	}
}

func TestFailedSignInMessageIsTranslated(t *testing.T) {
	e := testenv.New(t, testenv.Mount("docs"))
	c := e.Anonymous()
	c.AcceptLanguage = "zh"
	rec := c.PostWithoutCSRF("/login", map[string][]string{"name": {"nobody"}, "password": {"x"}})
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "登录失败") {
		t.Errorf("sign-in failure = %d:\n%s", rec.Code, rec.Body.String())
	}
}
