package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/regb/workitem/internal/model"
)

// BareImportOptions describes an independent import. Dirty source files are
// deliberately excluded. OriginURL is the upstream, never the local source.
type BareImportOptions = model.BareImportOptions

var objectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidateOwnedBare checks the location used by the coordinator before it
// registers a store. It rejects symlink roots and external object dependencies.
func (c Client) ValidateOwnedBare(ctx context.Context, path, branch string) error {
	return c.validateOwnedBare(ctx, path, branch, false)
}

// Observation and metadata edits do not advance an integration ref and may
// inspect an integration branch even when it is checked out.
func (c Client) ValidateOwnedBareObservation(ctx context.Context, path, branch string) error {
	return c.validateOwnedBare(ctx, path, branch, true)
}

func (c Client) validateOwnedBare(ctx context.Context, path, branch string, allowCheckedOut bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("owned Git store must be a directory, not a symlink")
	}
	for _, entry := range []string{"objects", "refs"} {
		info, err := os.Lstat(filepath.Join(path, entry))
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("owned Git %s must be a directory, not a symlink", entry)
		}
	}
	if err := filepath.WalkDir(path, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("owned Git metadata must not use symlinks: %s", name)
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := c.output(ctx, path, "check-ref-format", "refs/heads/"+branch); err != nil {
		return err
	}
	bare, err := c.output(ctx, path, "rev-parse", "--is-bare-repository")
	if err != nil {
		return err
	}
	if strings.TrimSpace(bare) != "true" {
		return fmt.Errorf("owned Git store must be bare")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	common, err := c.output(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	if filepath.Clean(strings.TrimSpace(common)) != filepath.Clean(realPath) {
		return fmt.Errorf("owned Git directory resolves outside its registered location")
	}
	common, err = c.output(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if filepath.Clean(strings.TrimSpace(common)) != filepath.Clean(realPath) {
		return fmt.Errorf("owned Git common directory resolves outside its registered location")
	}
	if _, err := os.Lstat(filepath.Join(path, "objects", "info", "alternates")); err == nil {
		return fmt.Errorf("owned Git store must not use external object alternates")
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err = c.output(ctx, path, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}"); err != nil {
		return err
	}
	worktrees, err := c.ListWorktrees(ctx, path)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		if !allowCheckedOut && !worktree.Bare && !worktree.Detached && worktree.Branch == branch {
			return fmt.Errorf("integration branch %s is checked out at %s", branch, worktree.Path)
		}
	}
	return nil
}

func (c Client) ImportBare(ctx context.Context, opts BareImportOptions) error {
	if strings.TrimSpace(opts.Source) == "" || !filepath.IsAbs(opts.Destination) || filepath.Clean(opts.Destination) != opts.Destination {
		return fmt.Errorf("source and absolute clean destination are required")
	}
	if err := model.ValidateRepositorySource(opts.Source); err != nil {
		return err
	}
	remote, err := model.SanitizeRemoteURL(opts.OriginURL)
	if err != nil {
		return err
	}
	// Never persist embedded HTTP credentials in the owned Git configuration.
	if remote != opts.OriginURL {
		return fmt.Errorf("origin URL must not contain credentials, query parameters, or fragments")
	}
	if remote != "" {
		if err := model.ValidateRepositorySource(remote); err != nil {
			return err
		}
	}
	if _, err := c.output(ctx, "", "check-ref-format", "refs/heads/"+opts.IntegrationBranch); err != nil {
		return fmt.Errorf("invalid integration branch: %w", err)
	}
	for _, sha := range opts.RequiredCommits {
		if !objectIDPattern.MatchString(sha) {
			return fmt.Errorf("invalid required commit %q", sha)
		}
	}
	if _, err := os.Lstat(opts.Destination); err == nil {
		return fmt.Errorf("repository destination already exists: %s", opts.Destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(opts.Destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".repository-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	path := filepath.Join(staging, "git")
	// --no-local avoids alternates and hardlinks to the onboarding source.
	if _, err := c.output(ctx, "", "clone", "--bare", "--no-local", "--", opts.Source, path); err != nil {
		return fmt.Errorf("import repository: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(path, "objects", "info", "alternates")); err == nil {
		return fmt.Errorf("imported repository unexpectedly depends on object alternates")
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := c.output(ctx, path, "config", "--remove-section", "remote.origin"); err != nil {
		return fmt.Errorf("remove onboarding-source remote: %w", err)
	}
	if remote != "" {
		if _, err := c.output(ctx, path, "remote", "add", "origin", remote); err != nil {
			return err
		}
		// Explicit remote-tracking refspec. Fetch never overwrites owned heads.
		if _, err := c.output(ctx, path, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return err
		}
	}
	if opts.UpstreamBranch != "" {
		if remote == "" {
			return fmt.Errorf("tracking requires a remote URL")
		}
		next, err := model.ParseRepositoryUpstream(remote, opts.UpstreamBranch)
		if err != nil {
			return err
		}
		if err := c.SetBranchUpstream(ctx, path, opts.IntegrationBranch, nil, next); err != nil {
			return err
		}
	}
	for _, sha := range opts.RequiredCommits {
		if _, err := c.output(ctx, path, "rev-parse", "--verify", sha+"^{commit}"); err != nil {
			if _, err := c.output(ctx, path, "fetch", "--no-tags", "--", opts.Source, sha); err != nil {
				return fmt.Errorf("import required commit %s: %w", sha, err)
			}
		}
		if _, err := c.output(ctx, path, "rev-parse", "--verify", sha+"^{commit}"); err != nil {
			return fmt.Errorf("required object %s is not a commit: %w", sha, err)
		}
		if _, err := c.output(ctx, path, "update-ref", "refs/wi/pins/"+sha, sha); err != nil {
			return err
		}
	}
	if _, err := c.output(ctx, path, "rev-parse", "--verify", "refs/heads/"+opts.IntegrationBranch+"^{commit}"); err != nil {
		return fmt.Errorf("integration branch %s is unavailable: %w", opts.IntegrationBranch, err)
	}
	if _, err := c.output(ctx, path, "symbolic-ref", "HEAD", "refs/heads/"+opts.IntegrationBranch); err != nil {
		return err
	}
	if _, err := c.output(ctx, path, "fsck", "--full", "--no-dangling"); err != nil {
		return fmt.Errorf("validate imported repository: %w", err)
	}
	if err := c.ValidateOwnedBare(ctx, path, opts.IntegrationBranch); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	// Both staging and final paths are wi-owned and on the same filesystem.
	if _, err := os.Lstat(opts.Destination); err == nil {
		return fmt.Errorf("repository destination appeared during import")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(path, opts.Destination); err != nil {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
