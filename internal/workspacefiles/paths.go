package workspacefiles

import "sort"

// MaxListedPaths bounds a checkout's file listing.
const MaxListedPaths = 10000

// Paths keeps the valid paths of a listing, sorted and without duplicates, up
// to MaxListedPaths. It reports whether more were left out.
func Paths(listed []string) ([]string, bool) {
	seen := make(map[string]bool, len(listed))
	paths := make([]string, 0, len(listed))
	for _, p := range listed {
		if ValidPath(p) == nil && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	if len(paths) > MaxListedPaths {
		return paths[:MaxListedPaths], true
	}
	return paths, false
}
