package testenv

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
)

// Client is one browser talking to the app: it keeps the session cookie and, once signed
// in, the CSRF token, so tests of later tickets can sign in and make writes in one line:
//
//	c := e.SignIn(testenv.DefaultUser)
//	rec := c.Post("/some/write", url.Values{"field": {"value"}})   // token attached
//	rec = c.PostWithoutCSRF("/some/write", nil)                    // must be refused
type Client struct {
	// CSRF is the signed-in session's token as read from the Root page, empty for an
	// Anonymous client.
	CSRF string
	// AcceptLanguage is sent as the Accept-Language header when set, like a browser's
	// language setting.
	AcceptLanguage string

	e      *Env
	cookie *http.Cookie
	lang   string // the language cookie the browser would have saved
	addr   string
}

// Anonymous returns a client with no session.
func (e *Env) Anonymous() *Client { return &Client{e: e, addr: "192.0.2.1:12345"} }

// SignIn signs name in through the real sign-in form and returns the signed-in client.
// It fails the test if sign-in does not succeed.
func (e *Env) SignIn(name string) *Client {
	e.t.Helper()
	c := e.Anonymous()
	rec := c.PostWithoutCSRF("/login", url.Values{"name": {name}, "password": {e.Password(name)}})
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("sign-in as %s = %d", name, rec.Code)
	}
	if !c.keepCookie(rec) {
		e.t.Fatalf("sign-in as %s set no session cookie", name)
	}
	root := c.Get("/")
	m := csrfMeta.FindStringSubmatch(root.Body.String())
	if root.Code != http.StatusOK || m == nil {
		e.t.Fatalf("Root page after sign-in = %d, no CSRF token", root.Code)
	}
	c.CSRF = m[1]
	return c
}

var csrfMeta = regexp.MustCompile(`<meta name="csrf-token" content="([^"]+)">`)

// From makes the client's requests come from the given remote address ("ip:port").
func (c *Client) From(addr string) *Client { c.addr = addr; return c }

func (c *Client) keepCookie(rec *httptest.ResponseRecorder) bool {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "nas_files_session" {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
			return c.cookie != nil
		}
	}
	return false
}

func (c *Client) do(r *http.Request) *httptest.ResponseRecorder {
	c.e.t.Helper()
	r.RemoteAddr = c.addr
	if c.cookie != nil {
		r.AddCookie(&http.Cookie{Name: c.cookie.Name, Value: c.cookie.Value})
	}
	if c.AcceptLanguage != "" {
		r.Header.Set("Accept-Language", c.AcceptLanguage)
	}
	if c.lang != "" {
		r.AddCookie(&http.Cookie{Name: "nas_files_lang", Value: c.lang})
	}
	rec := c.e.Do(r)
	c.keepCookie(rec)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "nas_files_lang" {
			c.lang = ck.Value
		}
	}
	return rec
}

// Get performs a GET as this client.
func (c *Client) Get(target string) *httptest.ResponseRecorder {
	c.e.t.Helper()
	return c.do(httptest.NewRequest(http.MethodGet, target, nil))
}

func (c *Client) form(method, target string, v url.Values) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// Post sends a form POST with this client's CSRF token in the csrf_token field.
func (c *Client) Post(target string, v url.Values) *httptest.ResponseRecorder {
	c.e.t.Helper()
	return c.PostWithToken(target, v, c.CSRF)
}

// PostWithToken sends a form POST with an explicit CSRF token (for wrong-token tests).
func (c *Client) PostWithToken(target string, v url.Values, token string) *httptest.ResponseRecorder {
	c.e.t.Helper()
	nv := url.Values{}
	for k, vs := range v {
		nv[k] = vs
	}
	nv.Set("csrf_token", token)
	return c.do(c.form(http.MethodPost, target, nv))
}

// PostWithHeaderToken sends a form POST with the token in the X-CSRF-Token header, as
// scripted (tus) requests do.
func (c *Client) PostWithHeaderToken(target string, v url.Values) *httptest.ResponseRecorder {
	c.e.t.Helper()
	r := c.form(http.MethodPost, target, v)
	r.Header.Set("X-CSRF-Token", c.CSRF)
	return c.do(r)
}

// PostWithoutCSRF sends a form POST with no CSRF token, for tests that a write is refused.
func (c *Client) PostWithoutCSRF(target string, v url.Values) *httptest.ResponseRecorder {
	c.e.t.Helper()
	return c.do(c.form(http.MethodPost, target, v))
}

// Do sends any request as this client, with the session cookie but no CSRF token added;
// add c.CSRF yourself (header X-CSRF-Token) for PATCH, DELETE and other methods.
func (c *Client) Do(r *http.Request) *httptest.ResponseRecorder {
	c.e.t.Helper()
	return c.do(r)
}
