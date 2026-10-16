package direnv

import (
	"os"
	"path/filepath"
	"strings"
)

func localRCWithin(root, path string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) || filepath.Base(path) != ".envrc" {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(canonical, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
