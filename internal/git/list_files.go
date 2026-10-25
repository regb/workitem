package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ListFiles lists a worktree's files: tracked, or untracked but not ignored.
func (c Client) ListFiles(ctx context.Context, dir string) ([]string, error) {
	return c.paths(ctx, dir, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
}

// ListCommitFiles lists the files of a commit. dir may be a bare repository,
// for work that has no worktree yet.
func (c Client) ListCommitFiles(ctx context.Context, dir, commit string) ([]string, error) {
	return c.paths(ctx, dir, "ls-tree", "-r", "-z", "--name-only", commit)
}

// paths runs a listing that prints NUL-separated paths. Only standard output
// is read, so a warning on standard error never becomes a path.
func (c Client) paths(ctx context.Context, dir string, args ...string) ([]string, error) {
	path := c.Path
	if path == "" {
		path = "git"
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"-C", dir}, args...)...)
	cmd.Env = repositoryEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	listed := strings.TrimRight(string(out), "\x00")
	if listed == "" {
		return []string{}, nil
	}
	return strings.Split(listed, "\x00"), nil
}
