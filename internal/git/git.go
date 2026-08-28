package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/model"
)

var ErrNotRepository = errors.New("not inside a git repository")

// RepositoryInfo remains an alias for compatibility; the neutral contract is
// owned by model so core application packages do not depend on this adapter.
type RepositoryInfo = model.RepositoryInfo

type WorktreeAddOptions = model.WorktreeAddOptions

type WorktreeInfo struct {
	Path     string
	Head     string
	Branch   string
	Bare     bool
	Detached bool
	Prunable string
}

type Client struct {
	Path string
}

func New(path string) Client {
	if path == "" {
		path = "git"
	}
	return Client{Path: path}
}

func (c Client) DetectRepository(ctx context.Context, dir, revision string) (RepositoryInfo, error) {
	if dir == "" {
		dir = "."
	}
	root, err := c.output(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	root, err = filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve repository root: %w", err)
	}

	common, err := c.output(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve git common directory: %w", err)
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve git common directory: %w", err)
	}

	rev := "HEAD"
	if strings.TrimSpace(revision) != "" {
		rev = strings.TrimSpace(revision)
	}
	commit, err := c.output(ctx, root, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve commit %q: %w", rev, err)
	}
	commit = strings.TrimSpace(commit)

	remote, err := c.output(ctx, root, "config", "--get", "remote.origin.url")
	if err != nil {
		remote = ""
	} else {
		remote = strings.TrimSpace(remote)
		remote, err = model.SanitizeRemoteURL(remote)
		if err != nil {
			return RepositoryInfo{}, fmt.Errorf("sanitize origin remote URL: %w", err)
		}
	}

	return RepositoryInfo{
		Repository: model.Repository{
			RootAtCreation:    root,
			GitCommonDir:      common,
			RemoteURL:         remote,
			CreatedFromCommit: commit,
		},
		Commit: commit,
	}, nil
}

// WorktreeSnapshot returns HEAD, branch, and porcelain-v2 status from one Git
// process. The status excludes branch metadata and is empty for a clean tree.
func (c Client) WorktreeSnapshot(ctx context.Context, dir string) (head, branch, status string, err error) {
	output, err := c.output(ctx, dir, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		return "", "", "", err
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	statusLines := []string{}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			head = strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid "))
			if head == "(initial)" {
				head = ""
			}
		case strings.HasPrefix(line, "# branch.head "):
			branch = strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			if branch == "(detached)" {
				branch = ""
			}
		case strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "":
		default:
			statusLines = append(statusLines, line)
		}
	}
	return head, branch, strings.Join(statusLines, "\n"), nil
}

func (c Client) Head(ctx context.Context, dir string) (string, error) {
	out, err := c.output(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (c Client) StatusPorcelain(ctx context.Context, dir string) (string, error) {
	out, err := c.output(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	return out, nil
}

func (c Client) CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := c.output(ctx, dir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (c Client) BranchExists(ctx context.Context, repoRoot, branch string) (bool, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false, fmt.Errorf("branch name is empty")
	}
	path := c.Path
	if path == "" {
		path = "git"
	}
	cmd := exec.CommandContext(ctx, path, "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	b, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	text := strings.TrimSpace(string(b))
	if text != "" {
		return false, fmt.Errorf("git show-ref --verify refs/heads/%s: %s: %w", branch, text, err)
	}
	return false, fmt.Errorf("git show-ref --verify refs/heads/%s: %w", branch, err)
}

func (c Client) WorktreeAdd(ctx context.Context, opts WorktreeAddOptions) error {
	if strings.TrimSpace(opts.RepoRoot) == "" {
		return fmt.Errorf("repository root is required")
	}
	if strings.TrimSpace(opts.Path) == "" {
		return fmt.Errorf("worktree path is required")
	}
	args := []string{"worktree", "add"}
	if opts.NewBranch {
		if strings.TrimSpace(opts.Branch) == "" {
			return fmt.Errorf("branch is required when creating a new worktree branch")
		}
		args = append(args, "-b", opts.Branch)
		args = append(args, opts.Path)
		if strings.TrimSpace(opts.StartPoint) != "" {
			args = append(args, opts.StartPoint)
		}
		_, err := c.output(ctx, opts.RepoRoot, args...)
		return err
	}

	args = append(args, opts.Path)
	if strings.TrimSpace(opts.Branch) != "" {
		args = append(args, opts.Branch)
	} else if strings.TrimSpace(opts.StartPoint) != "" {
		args = append(args, opts.StartPoint)
	}
	_, err := c.output(ctx, opts.RepoRoot, args...)
	return err
}

func (c Client) Switch(ctx context.Context, dir, branch, startPoint string, create bool) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("branch name is empty")
	}
	args := []string{"switch"}
	if create {
		args = append(args, "-c", branch)
		if strings.TrimSpace(startPoint) != "" {
			args = append(args, strings.TrimSpace(startPoint))
		}
	} else {
		args = append(args, branch)
	}
	_, err := c.output(ctx, dir, args...)
	return err
}

func (c Client) WorktreeRemove(ctx context.Context, repoRoot, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := c.output(ctx, repoRoot, args...)
	return err
}

func (c Client) DeleteBranch(ctx context.Context, repoRoot, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := c.output(ctx, repoRoot, "branch", flag, branch)
	return err
}

func (c Client) DefaultBranch(ctx context.Context, repoRoot string) (string, error) {
	if out, err := c.output(ctx, repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		branch := strings.TrimSpace(out)
		if _, name, ok := strings.Cut(branch, "/"); ok {
			branch = name
		}
		if exists, err := c.BranchExists(ctx, repoRoot, branch); err != nil {
			return "", err
		} else if exists {
			return branch, nil
		}
	}
	for _, branch := range []string{"main", "master", "develop", "trunk"} {
		exists, err := c.BranchExists(ctx, repoRoot, branch)
		if err != nil {
			return "", err
		}
		if exists {
			return branch, nil
		}
	}
	return "", fmt.Errorf("could not determine repository default branch; pass a target branch explicitly")
}

func (c Client) ResolveBranchSHA(ctx context.Context, repoRoot, branch string) (string, error) {
	return c.RevParse(ctx, repoRoot, "refs/heads/"+strings.TrimSpace(branch)+"^{commit}")
}

func (c Client) RevParse(ctx context.Context, dir, rev string) (string, error) {
	out, err := c.output(ctx, dir, "rev-parse", "--verify", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (c Client) EnsureClean(ctx context.Context, dir string) error {
	out, err := c.output(ctx, dir, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("checkout has staged, unstaged, untracked, or unmerged changes")
	}
	return nil
}

func (c Client) EnsureNoOperationInProgress(ctx context.Context, dir, action string) error {
	markers := []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"}
	for _, marker := range markers {
		path, err := c.output(ctx, dir, "rev-parse", "--git-path", marker)
		if err != nil {
			return err
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("cannot %s while Git operation marker %s exists", action, marker)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect Git operation marker %s: %w", marker, err)
		}
	}
	return nil
}

func (c Client) Rebase(ctx context.Context, dir, target string) error {
	_, err := c.output(ctx, dir, "rebase", "refs/heads/"+strings.TrimSpace(target))
	return err
}

func (c Client) RebaseAbort(ctx context.Context, dir string) error {
	_, err := c.output(ctx, dir, "rebase", "--abort")
	return err
}

func (c Client) ResetHard(ctx context.Context, dir, rev string) error {
	_, err := c.output(ctx, dir, "reset", "--hard", rev)
	return err
}

func (c Client) IsAncestor(ctx context.Context, repoRoot, ancestor, descendant string) (bool, error) {
	path := c.Path
	if path == "" {
		path = "git"
	}
	cmd := exec.CommandContext(ctx, path, "-C", repoRoot, "merge-base", "--is-ancestor", ancestor, descendant)
	b, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	if text := strings.TrimSpace(string(b)); text != "" {
		return false, fmt.Errorf("git merge-base --is-ancestor: %s: %w", text, err)
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

func (c Client) UpdateRef(ctx context.Context, repoRoot, ref, newSHA, oldSHA, message string) error {
	_, err := c.output(ctx, repoRoot, "update-ref", "-m", message, ref, newSHA, oldSHA)
	return err
}

func (c Client) ListWorktrees(ctx context.Context, repoRoot string) ([]WorktreeInfo, error) {
	out, err := c.output(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return ParseWorktreeList(out), nil
}

func ParseWorktreeList(out string) []WorktreeInfo {
	blocks := strings.Split(strings.TrimSpace(out), "\n\n")
	worktrees := make([]WorktreeInfo, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var wt WorktreeInfo
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				wt.Path = value
			case "HEAD":
				wt.Head = value
			case "branch":
				wt.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "bare":
				wt.Bare = true
			case "detached":
				wt.Detached = true
			case "prunable":
				wt.Prunable = value
			}
		}
		if wt.Path != "" {
			worktrees = append(worktrees, wt)
		}
	}
	return worktrees
}

func (c Client) RepositoryHome(ctx context.Context, repoRoot string) (model.RepositoryHomeInfo, error) {
	worktrees, err := c.ListWorktrees(ctx, repoRoot)
	if err != nil {
		return model.RepositoryHomeInfo{}, err
	}
	if len(worktrees) == 0 || worktrees[0].Bare {
		return model.RepositoryHomeInfo{Bare: len(worktrees) > 0 && worktrees[0].Bare}, nil
	}
	primary := worktrees[0]
	return model.RepositoryHomeInfo{Path: primary.Path, Branch: primary.Branch, Detached: primary.Detached}, nil
}

func (c Client) TargetWorktreeForBranch(ctx context.Context, repoRoot, branch string) (*WorktreeInfo, error) {
	worktrees, err := c.ListWorktrees(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	for _, wt := range worktrees {
		if wt.Branch == branch {
			copy := wt
			return &copy, nil
		}
	}
	return nil, nil
}

func (c Client) DiffNameOnly(ctx context.Context, repoRoot, oldSHA, newSHA string) ([]string, error) {
	out, err := c.output(ctx, repoRoot, "diff", "--name-only", "-z", oldSHA, newSHA)
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

func (c Client) StatusPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := c.output(ctx, dir, "status", "--porcelain=v1", "-z", "-uall")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	paths := []string{}
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if entry == "" || len(entry) < 3 {
			continue
		}
		status := entry[:2]
		paths = append(paths, entry[3:])
		if (status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C') && i+1 < len(fields) && fields[i+1] != "" {
			i++
			paths = append(paths, fields[i])
		}
	}
	return paths, nil
}

func (c Client) UpdateIndexRefresh(ctx context.Context, dir string) error {
	_, err := c.output(ctx, dir, "update-index", "-q", "--refresh")
	return err
}

func (c Client) ReadTreeMergeUpdate(ctx context.Context, dir, oldSHA, newSHA string) error {
	_, err := c.output(ctx, dir, "-c", "submodule.recurse=false", "read-tree", "-m", "-u", oldSHA, newSHA)
	return err
}

func splitNUL(out string) []string {
	parts := strings.Split(out, "\x00")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func (c Client) output(ctx context.Context, dir string, args ...string) (string, error) {
	path := c.Path
	if path == "" {
		path = "git"
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"-C", dir}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(b))
		if text != "" {
			return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), text, err)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(b), nil
}
