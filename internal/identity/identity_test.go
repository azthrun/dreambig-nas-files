package identity_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azthrun/dreambig-nas-files/internal/identity"
	"golang.org/x/crypto/bcrypt"
)

func hash(t *testing.T, pw string) string {
	t.Helper()
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAuthenticateChecksNameAndPassword(t *testing.T) {
	pw := strings.Repeat("ab", 6)
	a, err := identity.Parse(strings.NewReader("# comment\n\nalice:" + hash(t, pw) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.Authenticate("alice", pw)
	if err != nil || u.Name != "alice" {
		t.Fatalf("valid sign-in: %v %v", u, err)
	}
	for _, c := range [][2]string{{"alice", "wrong"}, {"nobody", pw}, {"", ""}, {"Alice", pw}} {
		if _, err := a.Authenticate(c[0], c[1]); !errors.Is(err, identity.ErrInvalidCredentials) {
			t.Errorf("Authenticate(%q) = %v, want ErrInvalidCredentials", c[0], err)
		}
	}
}

func TestMalformedUsersFileIsRefused(t *testing.T) {
	good := hash(t, "x")
	cases := map[string]string{
		"empty file":        "",
		"only comments":     "# nothing\n",
		"no colon":          "alice\n",
		"empty name":        ":" + good + "\n",
		"plain text hash":   "alice:secret\n",
		"md5 htpasswd hash": "alice:$apr1$abc$def\n",
		"duplicate name":    "alice:" + good + "\nalice:" + good + "\n",
		"bad line after ok": "alice:" + good + "\nbob\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := identity.Parse(strings.NewReader(content))
			if err == nil {
				t.Fatal("malformed file accepted")
			}
			if strings.Contains(err.Error(), good) {
				t.Errorf("error leaks a hash: %v", err)
			}
		})
	}
}

func TestLoadReadsAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "users")
	if err := os.WriteFile(p, []byte("alice:"+hash(t, "x")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Load(p); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Load(p + ".missing"); err == nil {
		t.Fatal("missing file accepted")
	}
}
