// Package identity answers one question: who is this name and password? It is the one
// narrow seam in front of sign-in (ADR-0004), so an external identity provider can
// replace the htpasswd file later without touching the web layer.
package identity

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is returned for an unknown name or a wrong password alike.
var ErrInvalidCredentials = errors.New("invalid credentials")

// User is a signed-in person.
type User struct {
	Name string
}

// Authenticator verifies a name and password.
type Authenticator interface {
	Authenticate(name, password string) (User, error)
}

// Htpasswd is an Authenticator backed by an htpasswd-format file of bcrypt hashes.
type Htpasswd struct {
	hashes map[string][]byte
	// dummy is compared against for unknown names so timing does not reveal them.
	dummy []byte
}

// Load reads and validates the users file at path.
func Load(path string) (*Htpasswd, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("users file: %w", err)
	}
	defer f.Close()
	h, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("users file: %w", err)
	}
	return h, nil
}

// Parse reads htpasswd-format lines "name:bcrypt-hash". Blank lines and lines starting
// with # are skipped. Any other line that is not a name and a bcrypt hash, a duplicate
// name, or a file with no Users is an error. Errors never contain hashes.
func Parse(r io.Reader) (*Htpasswd, error) {
	h := &Htpasswd{hashes: map[string][]byte{}}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, hash, ok := strings.Cut(line, ":")
		if !ok || name == "" {
			return nil, fmt.Errorf("line %d: want name:bcrypt-hash", n)
		}
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil {
			return nil, fmt.Errorf("line %d: password for %q is not a bcrypt hash (generate with htpasswd -B)", n, name)
		}
		if _, dup := h.hashes[name]; dup {
			return nil, fmt.Errorf("line %d: duplicate user %q", n, name)
		}
		h.hashes[name] = []byte(hash)
		if h.dummy == nil {
			d, err := bcrypt.GenerateFromPassword([]byte("dummy"), cost)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			h.dummy = d
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(h.hashes) == 0 {
		return nil, errors.New("no Users defined")
	}
	return h, nil
}

// Names lists the Users, sorted.
func (h *Htpasswd) Names() []string {
	names := make([]string, 0, len(h.hashes))
	for n := range h.hashes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Authenticate implements Authenticator.
func (h *Htpasswd) Authenticate(name, password string) (User, error) {
	hash, ok := h.hashes[name]
	if !ok {
		_ = bcrypt.CompareHashAndPassword(h.dummy, []byte(password))
		return User{}, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return User{}, ErrInvalidCredentials
	}
	return User{Name: name}, nil
}
