package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/regb/workitem/internal/model"
)

func (c Client) trackingValue(ctx context.Context, dir, key string) (string, error) {
	out, err := c.output(ctx, dir, "config", "--local", "--includes", "--null", "--get-all", key)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil {
			return "", nil
		}
		return "", err
	}
	values := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(values) != 1 || values[0] == "" {
		return "", fmt.Errorf("Git tracking key %s must have one nonempty value", key)
	}
	return values[0], nil
}

// BranchUpstream observes repository-local Git tracking. It does not fetch.
// A missing pair means local-only; incomplete/unsafe configuration is an error.
func (c Client) BranchUpstream(ctx context.Context, dir, branch string) (*model.RepositoryUpstream, error) {
	if err := model.ValidateRepositoryBranch(branch); err != nil {
		return nil, err
	}
	remote, err := c.trackingValue(ctx, dir, "branch."+branch+".remote")
	if err != nil {
		return nil, err
	}
	merge, err := c.trackingValue(ctx, dir, "branch."+branch+".merge")
	if err != nil {
		return nil, err
	}
	if remote == "" && merge == "" {
		return nil, nil
	}
	if remote == "" || !strings.HasPrefix(merge, "refs/heads/") {
		return nil, fmt.Errorf("incomplete Git tracking for %s; configure its remote and merge branch", branch)
	}
	if remote == "." || strings.Contains(remote, "/") || model.ValidateRepositoryBranch(remote) != nil {
		return nil, fmt.Errorf("Git upstream must use a named remote, not local branch tracking")
	}
	url, err := c.trackingValue(ctx, dir, "remote."+remote+".url")
	if err != nil {
		return nil, err
	}
	if url == "" {
		return nil, fmt.Errorf("Git upstream remote %s has no URL", remote)
	}
	if !strings.Contains(url, ":") && !filepath.IsAbs(url) {
		url = filepath.Join(dir, url)
	}
	out, err := c.output(ctx, dir, "for-each-ref", "--format=%(upstream)", "refs/heads/"+branch)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(out)
	if !strings.HasPrefix(ref, "refs/remotes/") || model.ValidateRepositoryBranch(strings.TrimPrefix(ref, "refs/remotes/")) != nil {
		return nil, fmt.Errorf("Git upstream fetch ref must be under refs/remotes/; configure remote.%s.fetch", remote)
	}
	result := &model.RepositoryUpstream{URL: url, Remote: remote, Branch: strings.TrimPrefix(merge, "refs/heads/"), TrackingRef: ref}
	return result, result.Validate()
}

// SetBranchUpstream edits origin and branch tracking in a single local config
// replacement. Git's config.lock serializes native writers. The expected
// observation and original bytes detect concurrent edits; unrelated keys remain.
func (c Client) SetBranchUpstream(ctx context.Context, dir, branch string, expected, next *model.RepositoryUpstream) (err error) {
	if err := model.ValidateRepositoryBranch(branch); err != nil {
		return err
	}
	if next != nil {
		if err := next.Validate(); err != nil {
			return err
		}
		if next.Remote != "" && next.Remote != "origin" {
			return fmt.Errorf("this command configures origin; use Git for other named remotes")
		}
	}
	config := filepath.Join(dir, "config")
	info, err := os.Lstat(config)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Git config must be a regular file, not a symlink")
	}
	original, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	lockPath := config + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("lock Git configuration: %w", err)
	}
	lockInfo, err := lock.Stat()
	if err != nil {
		lock.Close()
		return err
	}
	committed := false
	defer func() {
		lock.Close()
		if !committed {
			if current, err := os.Lstat(lockPath); err == nil && os.SameFile(lockInfo, current) {
				os.Remove(lockPath)
			}
		}
	}()
	current, err := c.BranchUpstream(ctx, dir, branch)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, expected) {
		return fmt.Errorf("Git upstream changed concurrently; retry with its current configuration")
	}
	keys := []string{"branch." + branch + ".remote", "branch." + branch + ".merge"}
	if next != nil {
		keys = append(keys, "remote.origin.url", "remote.origin.fetch")
	}
	for _, key := range keys {
		var outputs []string
		for _, includes := range []string{"--no-includes", "--includes"} {
			out, err := c.output(ctx, dir, "config", "--local", includes, "--null", "--get-all", key)
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil {
					return err
				}
			}
			outputs = append(outputs, out)
		}
		if outputs[0] != outputs[1] {
			return fmt.Errorf("tracking field %s is defined through a Git include; edit it with Git instead", key)
		}
	}
	staging, err := os.MkdirTemp(dir, ".wi-config-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	draft := filepath.Join(staging, "config")
	if err := os.WriteFile(draft, original, 0600); err != nil {
		return err
	}
	edit := func(key, value string, remove bool) error {
		args := []string{"config", "--file", draft, "--no-includes"}
		if remove {
			args = append(args, "--unset-all", key)
		} else {
			args = append(args, "--replace-all", key, value)
		}
		_, err := c.output(ctx, dir, args...)
		if remove {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 5 {
				return nil
			}
		}
		return err
	}
	if next == nil {
		for _, key := range []string{"branch." + branch + ".remote", "branch." + branch + ".merge"} {
			if err := edit(key, "", true); err != nil {
				return err
			}
		}
	} else {
		for _, pair := range [][2]string{{"remote.origin.url", next.URL}, {"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"}, {"branch." + branch + ".remote", "origin"}, {"branch." + branch + ".merge", "refs/heads/" + next.Branch}} {
			if err := edit(pair[0], pair[1], false); err != nil {
				return err
			}
		}
	}
	edited, err := os.ReadFile(draft)
	if err != nil {
		return err
	}
	latest, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	latestInfo, err := os.Lstat(config)
	if err != nil {
		return err
	}
	if !os.SameFile(info, latestInfo) || !bytes.Equal(original, latest) {
		return fmt.Errorf("Git config changed concurrently; staged edit was not applied")
	}
	if _, err := lock.Write(edited); err != nil {
		return err
	}
	if err := lock.Sync(); err != nil {
		return err
	}
	if err := lock.Close(); err != nil {
		return err
	}
	lockCurrent, err := os.Lstat(lockPath)
	if err != nil || !os.SameFile(lockInfo, lockCurrent) {
		return fmt.Errorf("Git configuration lock identity changed; edit was not applied")
	}
	if err := os.Rename(lockPath, config); err != nil {
		return err
	}
	committed = true
	parent, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("Git configuration updated, but directory sync failed: %w", err)
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return fmt.Errorf("Git configuration updated, but directory sync failed: %w", err)
	}
	return nil
}
