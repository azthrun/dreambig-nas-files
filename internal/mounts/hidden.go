package mounts

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

const (
	// TrashDir is the Trash folder in each writable Mount.
	TrashDir = ".nas-files-trash"
	// StagingDir is the Staging Area folder in each writable Mount.
	StagingDir = ".nas-files-staging"
	// mcpTrashDir is the sibling dreambig-nas-mcp's Trash; Mounts may overlap with it.
	mcpTrashDir = ".nas-mcp-trash"
)

// Fold normalises a name for comparison: NFC, then lower case. Names that fold equal
// clash on a case-insensitive client.
func Fold(name string) string {
	return strings.ToLower(norm.NFC.String(name))
}

// IsHiddenName reports whether name is a Hidden Entry name: DSM and service folders
// that Users must never see or address (GLOSSARY.md). Anything starting with "@" or
// "#" is hidden, as are .DS_Store and the Trash and Staging Area folders.
func IsHiddenName(name string) bool {
	if strings.HasPrefix(name, "@") || strings.HasPrefix(name, "#") {
		return true
	}
	switch Fold(name) {
	case ".ds_store", TrashDir, StagingDir, mcpTrashDir:
		return true
	}
	return false
}

// isReservedMountName is stricter than IsHiddenName: no Mount may start with ".".
func isReservedMountName(name string) bool {
	return strings.HasPrefix(name, ".") || IsHiddenName(name)
}
