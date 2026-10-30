// Package listing sorts and pages the entries of a folder. Reading and filtering the
// folder (Hidden Entries, escaping symlinks) is the Path resolver's job; this package
// only orders what the resolver returned.
package listing

import (
	"cmp"
	"slices"
	"strings"

	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/resolver"
)

// PageSize is how many entries one page shows, so a folder of thousands stays light.
const PageSize = 500

// Key is the column a listing is sorted by.
type Key string

// Sort keys.
const (
	ByName     Key = "name"
	BySize     Key = "size"
	ByModified Key = "modified"
)

// Order is the sort direction.
type Order string

// Sort directions.
const (
	Asc  Order = "asc"
	Desc Order = "desc"
)

// ParseKey returns the key named by s, or ByName for anything unknown.
func ParseKey(s string) Key {
	switch Key(s) {
	case BySize, ByModified:
		return Key(s)
	}
	return ByName
}

// ParseOrder returns the order named by s, or Asc for anything unknown.
func ParseOrder(s string) Order {
	if Order(s) == Desc {
		return Desc
	}
	return Asc
}

// Sort orders entries in place: folders first, then files, whatever the key or order.
// Within each group the key and order apply; ties fall back to name. Folders have no
// size, so under BySize they order by name.
func Sort(entries []resolver.Entry, key Key, order Order) {
	slices.SortFunc(entries, func(a, b resolver.Entry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		var c int
		switch {
		case key == BySize && !a.IsDir:
			c = cmp.Compare(a.Size, b.Size)
		case key == ByModified:
			c = a.ModTime.Compare(b.ModTime)
		}
		if order == Desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return byName(a.Name, b.Name, key == ByName && order == Desc)
	})
}

func byName(a, b string, desc bool) int {
	c := cmp.Compare(mounts.Fold(a), mounts.Fold(b))
	if c == 0 {
		c = strings.Compare(a, b)
	}
	if desc {
		return -c
	}
	return c
}

// Page returns the 1-based page of entries and the total number of pages (at least 1).
// A page past the end is the last page; one below 1 is the first.
func Page(entries []resolver.Entry, page int) (shown []resolver.Entry, current, pages int) {
	pages = max(1, (len(entries)+PageSize-1)/PageSize)
	current = min(max(page, 1), pages)
	lo := (current - 1) * PageSize
	return entries[lo:min(lo+PageSize, len(entries))], current, pages
}
