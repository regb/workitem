package git

import (
	"context"
	"fmt"
	"os"
)

// AddMergeSnapshot materializes an exact source commit without checking out or
// updating its branch. The caller owns the private destination and its cleanup.
func (c Client) AddMergeSnapshot(ctx context.Context, root, path, sha string) error {
	_, err := c.output(ctx, root, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--detach", "--", path, sha)
	return err
}

// RebaseMergeSnapshot must not honor rebase.updateRefs: that would rewrite the
// original checked-out branch behind its dirty index and working files.
func (c Client) RebaseMergeSnapshot(ctx context.Context, path, targetSHA string) error {
	_, err := c.output(ctx, path, "-c", "core.hooksPath="+os.DevNull, "-c", "rebase.updateRefs=false", "-c", "core.editor=true", "rebase", targetSHA)
	return err
}

// HasMergeSourceChanges includes ignored files. A rebase can overwrite ignored
// data when the target starts tracking its path, even when ordinary status is clean.
func (c Client) HasMergeSourceChanges(ctx context.Context, path string) (bool, error) {
	out, err := c.output(ctx, path, "status", "--porcelain=v1", "-z", "--ignored=matching", "--untracked-files=normal", "--ignore-submodules=none")
	return out != "", err
}

func (c Client) EnsureNoUnmerged(ctx context.Context, path string) error {
	out, err := c.output(ctx, path, "ls-files", "--unmerged", "-z")
	if err != nil {
		return err
	}
	if out != "" {
		return fmt.Errorf("source index has unmerged entries; resolve conflicts before merging")
	}
	return nil
}
