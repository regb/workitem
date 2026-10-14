package git

import (
	"context"
	"errors"
	"fmt"
	"github.com/regb/workitem/internal/model"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// OriginTrackingBranch returns the configured origin branch without requiring
// a fresh remote-tracking ref. Other remotes are not onboarding defaults.
func (c Client) OriginTrackingBranch(ctx context.Context, path, branch string) (string, error) {
	out, err := c.output(ctx, path, "for-each-ref", "--format=%(upstream:remotename)%09%(upstream:remoteref)", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	remote, ref, ok := strings.Cut(strings.TrimSpace(out), "\t")
	if !ok || remote != "origin" || !strings.HasPrefix(ref, "refs/heads/") {
		return "", nil
	}
	tracked := strings.TrimPrefix(ref, "refs/heads/")
	return tracked, model.ValidateRepositoryBranch(tracked)
}

func ValidCommitSHA(sha string) bool { return objectIDPattern.MatchString(sha) }

func IntegrationLockPath(gitDir string) string { return filepath.Join(gitDir, "wi-integration.lock") }

func (c Client) FetchRepositoryUpstream(ctx context.Context, dir string, u model.RepositoryUpstream) error {
	if err := u.Validate(); err != nil {
		return err
	}
	if u.Remote == "" || !strings.HasPrefix(u.Ref(), "refs/remotes/") {
		return fmt.Errorf("fetch requires an observed Git upstream")
	}
	resolved, err := c.output(ctx, dir, "remote", "get-url", "--", u.Remote)
	if err != nil {
		return err
	}
	if err := model.ValidateRepositorySource(strings.TrimSpace(resolved)); err != nil {
		return err
	}
	_, err = c.output(ctx, dir, "fetch", "--atomic", "--no-tags", "--no-write-fetch-head", "--", u.Remote, "+refs/heads/"+u.Branch+":"+u.Ref())
	return err
}
func (c Client) AheadBehind(ctx context.Context, dir, left, right string) (int, int, error) {
	out, err := c.output(ctx, dir, "rev-list", "--left-right", "--count", left+"..."+right)
	if err != nil {
		return 0, 0, err
	}
	p := strings.Fields(out)
	if len(p) != 2 {
		return 0, 0, fmt.Errorf("invalid Git ahead/behind response")
	}
	a, err := strconv.Atoi(p[0])
	if err != nil {
		return 0, 0, err
	}
	b, err := strconv.Atoi(p[1])
	return a, b, err
}
func (c Client) ReferenceExists(ctx context.Context, dir, ref string) (bool, error) {
	_, err := c.output(ctx, dir, "show-ref", "--verify", "--quiet", ref)
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (c Client) LocalOnlyCommits(ctx context.Context, dir, local, upstream string) ([]string, error) {
	out, err := c.output(ctx, dir, "log", "--format=%H", "--max-count=20", upstream+".."+local)
	return strings.Fields(out), err
}

func (c Client) EqualTrees(ctx context.Context, dir, a, b string) (bool, error) {
	x, err := c.RevParse(ctx, dir, a+"^{tree}")
	if err != nil {
		return false, err
	}
	y, err := c.RevParse(ctx, dir, b+"^{tree}")
	return x == y, err
}
func (c Client) RecoveryRef(ctx context.Context, dir, ref, sha string) error {
	_, err := c.output(ctx, dir, "update-ref", ref, sha, strings.Repeat("0", len(sha)))
	return err
}
func (c Client) AddReconciliation(ctx context.Context, dir, path, sha string) error {
	_, err := c.output(ctx, dir, "worktree", "add", "--detach", "--", path, sha)
	return err
}
func (c Client) MergeReconciliation(ctx context.Context, path, sha string) error {
	_, err := c.output(ctx, path, "-c", "core.hooksPath="+os.DevNull, "-c", "core.editor=true", "merge", "--no-ff", "--no-edit", "--", sha)
	return err
}
func (c Client) CommitReconciliation(ctx context.Context, path string) error {
	_, err := c.output(ctx, path, "-c", "core.hooksPath="+os.DevNull, "-c", "core.editor=true", "commit", "--no-edit")
	return err
}
func (c Client) AbortReconciliation(ctx context.Context, path string) error {
	_, err := c.output(ctx, path, "merge", "--abort")
	return err
}
func (c Client) EnsureReconciliationOperation(ctx context.Context, path string) error {
	for _, marker := range []string{"rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"} {
		location, err := c.output(ctx, path, "rev-parse", "--git-path", marker)
		if err != nil {
			return err
		}
		location = strings.TrimSpace(location)
		if !filepath.IsAbs(location) {
			location = filepath.Join(path, location)
		}
		if _, err := os.Stat(location); err == nil {
			return fmt.Errorf("cannot reconcile while Git operation marker %s exists", marker)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (c Client) MergeInProgress(ctx context.Context, path string) (bool, error) {
	marker, err := c.output(ctx, path, "rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil {
		return false, err
	}
	p := strings.TrimSpace(marker)
	if !filepath.IsAbs(p) {
		p = filepath.Join(path, p)
	}
	_, err = os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
