// Package scrub keeps absolute NAS paths out of errors (CODING_STANDARDS.md: errors never
// echo them). Errors from the os package carry the path they failed on; Path drops it.
package scrub

import (
	"errors"
	"os"
)

// Path returns err without the file path that os.PathError and os.LinkError carry, so
// only the cause is left ("permission denied"). Other errors pass through unchanged.
func Path(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}
