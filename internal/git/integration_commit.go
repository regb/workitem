package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

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
func (c Client) IgnoredPaths(ctx context.Context, path string) ([]string, error) {
	out, err := c.output(ctx, path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	return strings.FieldsFunc(out, func(r rune) bool { return r == 0 }), nil
}

// RebaseOnto uses the fenced target commit, never a branch that can move during
// rebase. Conflicts remain in the real checkout for ordinary Git resolution.
func (c Client) RebaseOnto(ctx context.Context, dir, target string) error {
	if !ValidCommitSHA(target) {
		return errors.New("full rebase target required")
	}
	_, err := c.output(ctx, dir, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "rebase.updateRefs=false", "rebase", "--no-update-refs", "--no-autostash", target)
	return err
}

// SquashCommit creates an ordinary commit with exactly one target parent. A
// private review snapshot is never a parent or an exported ordinary ancestor.
func (c Client) SquashCommit(ctx context.Context, dir, tree, parent, message string) (string, error) {
	if !ValidCommitSHA(tree) || !ValidCommitSHA(parent) || strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) {
		return "", errors.New("tree, parent and nonempty squash message required")
	}
	path := c.Path
	if path == "" {
		path = "git"
	}
	cmd := exec.CommandContext(ctx, path, "-C", dir, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit-tree", tree, "-p", parent)
	cmd.Env = repositoryEnvironment()
	cmd.Stdin = strings.NewReader(message + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create squash commit: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
