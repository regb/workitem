package cli

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	gitpkg "github.com/regb/workitem/internal/git"
)

// Contextual Git expressions are client input, not names in the daemon's bare
// store. Resolve them read-only in the verified current checkout and send a SHA.
func rpcBase(ctx context.Context, cfg Config, repository, revision string) (string, error) {
	if revision == "" || revision == "^" {
		return "", nil
	}
	expression, relative := gitpkg.WorktreeRelativeRevision(revision)
	if !relative {
		return revision, nil
	}
	current, err := rpcSelect(ctx, cfg, "")
	if err != nil {
		return "", fmt.Errorf("base %q requires a current item with a claimed worktree: %w", revision, err)
	}
	item, err := rpcCurrentItem(ctx, cfg, current)
	if err != nil {
		return "", err
	}
	if item.Repository == nil || item.Repository.Id != repository {
		return "", fmt.Errorf("base %q belongs to the current worktree's repository, not the selected repository", revision)
	}
	workspace, err := cfg.API.Workspaces.GetWorkspace(ctx, rpcRequest(cfg, &v1.GetWorkspaceRequest{ItemId: current}))
	if err != nil {
		return "", err
	}
	claim := workspace.Msg.Checkout.GetClaim()
	if claim == nil {
		return "", fmt.Errorf("base %q requires a materialized worktree", revision)
	}
	root, err := filepath.EvalSymlinks(claim.Path)
	if err != nil {
		return "", err
	}
	cwd, err := filepath.EvalSymlinks(cfg.CWD)
	if err != nil {
		return "", err
	}
	distance, err := filepath.Rel(root, cwd)
	if err != nil || distance == ".." || strings.HasPrefix(distance, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("base %q requires running inside the current item's claimed worktree", revision)
	}
	git := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
		command.Env = gitpkg.RepositoryEnvironment()
		out, err := command.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("resolve base %q: %w: %s", revision, err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil || top != root {
		return "", fmt.Errorf("current directory is not the selected item's Git worktree")
	}
	return git("rev-parse", "--verify", "--end-of-options", expression+"^{commit}")
}
