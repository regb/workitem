package model

import (
	"path/filepath"
	"strconv"
	"strings"
)

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
	if err != nil || filepath.Dir(absolute) != root {
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
