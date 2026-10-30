// Package access holds Access: what a User may do in one Mount (GLOSSARY.md). The owner
// writes it in a JSON file, {"<user>":{"<mount>":"read"|"write"|"none"}}; anything not
// listed is none. A read-only Mount caps every User at read.
//
// Later tickets do not use this package directly: they call the server's authorize
// helper, which judges the REAL target's Mount.
package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
)

// Level is what a User may do in a Mount. Levels are ordered: None < Read < Write.
type Level int

const (
	None Level = iota
	Read
	Write
)

func (l Level) String() string {
	switch l {
	case Read:
		return "read"
	case Write:
		return "write"
	}
	return "none"
}

func parseLevel(s string) (Level, bool) {
	switch s {
	case "none":
		return None, true
	case "read":
		return Read, true
	case "write":
		return Write, true
	}
	return None, false
}

// Table is the loaded Access file.
type Table struct {
	grants map[string]map[string]Level // user -> mount name -> level
}

// Level is the User's effective Access in m: the granted level, capped at Read when the
// Mount is read-only. Unknown Users and unlisted Mounts get None.
func (t *Table) Level(user string, m mounts.Mount) Level {
	l := t.grants[user][m.Name]
	if m.ReadOnly && l > Read {
		return Read
	}
	return l
}

// Load reads and validates the Access file at path against the known Users and Mounts.
// A missing or malformed file, an unknown User or Mount, or an unknown level is an
// error, so a typo never silently grants or hides anything.
func Load(path string, users []string, ms []mounts.Mount) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Access file: %w", scrubPath(err))
	}
	defer f.Close()
	t, err := Parse(f, users, ms)
	if err != nil {
		return nil, fmt.Errorf("Access file: %w", err)
	}
	return t, nil
}

func scrubPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// Parse is Load for an already-open file.
func Parse(r io.Reader, users []string, ms []mounts.Mount) (*Table, error) {
	dec := json.NewDecoder(r)
	t := &Table{grants: map[string]map[string]Level{}}
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	for dec.More() {
		user, err := readString(dec)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(users, user) {
			return nil, fmt.Errorf("unknown User %q (not in the users file)", user)
		}
		if _, dup := t.grants[user]; dup {
			return nil, fmt.Errorf("User %q listed twice", user)
		}
		grants := map[string]Level{}
		t.grants[user] = grants
		if err := expectDelim(dec, '{'); err != nil {
			return nil, fmt.Errorf("User %q: %w", user, err)
		}
		for dec.More() {
			mount, err := readString(dec)
			if err != nil {
				return nil, fmt.Errorf("User %q: %w", user, err)
			}
			if !slices.ContainsFunc(ms, func(m mounts.Mount) bool { return m.Name == mount }) {
				return nil, fmt.Errorf("User %q: unknown Mount %q", user, mount)
			}
			if _, dup := grants[mount]; dup {
				return nil, fmt.Errorf("User %q: Mount %q listed twice", user, mount)
			}
			var word string
			if err := dec.Decode(&word); err != nil {
				return nil, fmt.Errorf("User %q, Mount %q: want \"none\", \"read\" or \"write\"", user, mount)
			}
			lvl, ok := parseLevel(word)
			if !ok {
				return nil, fmt.Errorf("User %q, Mount %q: unknown level %q (want none, read or write)", user, mount, word)
			}
			grants[mount] = lvl
		}
		if err := expectDelim(dec, '}'); err != nil {
			return nil, fmt.Errorf("User %q: %w", user, err)
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the closing brace")
	}
	return t, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("malformed JSON: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("malformed: want %q", string(want))
	}
	return nil
}

func readString(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", fmt.Errorf("malformed JSON: %w", err)
	}
	s, ok := tok.(string)
	if !ok {
		return "", errors.New("malformed: want a name")
	}
	return s, nil
}
