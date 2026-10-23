package model

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SameDir reports whether two paths name the same directory, also through
// symlinks: Git reports resolved paths, and a data root may sit behind a
// symlink (on macOS, /var and /tmp are under /private). A directory that does
// not exist compares by its cleaned path.
func SameDir(a, b string) bool {
	if infoA, err := os.Stat(a); err == nil {
		if infoB, err := os.Stat(b); err == nil {
			return os.SameFile(infoA, infoB)
		}
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// IsManagedSlotPath recognizes Wi's reserved slot namespace, including warm
// slots without an active claim. Git worktree registration alone is not ownership.
func IsManagedSlotPath(worktreesRoot, repositoryKey, path string) bool {
	if worktreesRoot == "" || repositoryKey == "" || path == "" || filepath.IsAbs(repositoryKey) || filepath.Base(repositoryKey) != repositoryKey || repositoryKey == "." || repositoryKey == ".." {
		return false
	}
	root, err := filepath.Abs(filepath.Join(worktreesRoot, repositoryKey))
	if err != nil {
		return false
	}
	absolute, err := filepath.Abs(path)
	if err != nil || !SameDir(filepath.Dir(absolute), root) {
		return false
	}
	return IsManagedSlotName(filepath.Base(absolute))
}

func IsManagedSlotName(name string) bool {
	if !strings.HasPrefix(name, "slot-") {
		return false
	}
	digits := strings.TrimPrefix(name, "slot-")
	if digits == "" {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(digits)
	return err == nil && n > 0
}
