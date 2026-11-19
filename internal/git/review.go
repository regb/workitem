package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/regb/workitem/internal/workspacefiles"
)

const ReviewPathLimit = 10000
const reviewOutputLimit = 8 << 20

// reviewOutput bounds both process lifetime and captured output. External diff
// tools, textconv and optional index writes are never used for review reads.
func (c Client) reviewOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, append([]string{"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-C", dir}, args...)...)
	// Partial clones must not turn a read-only review into a lazy fetch.
	cmd.Env = append(repositoryEnvironment(), "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1")
	cmd.WaitDelay = time.Second
	out := limitedReviewBuffer{limit: reviewOutputLimit}
	stderr := limitedReviewBuffer{limit: 64 << 10}
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git review: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

type limitedReviewBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedReviewBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("Git review output limit exceeded")
	}
	return b.Buffer.Write(p)
}

func (c Client) ReviewResolve(ctx context.Context, dir, ref string) (string, error) {
	out, err := c.reviewOutput(ctx, dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(string(out)), err
}
func (c Client) ReviewMergeBase(ctx context.Context, dir, head, integration string) (string, error) {
	out, err := c.reviewOutput(ctx, dir, "merge-base", head, integration)
	return strings.TrimSpace(string(out)), err
}
func (c Client) ReviewParent(ctx context.Context, dir, commit string) (string, error) {
	out, err := c.reviewOutput(ctx, dir, "rev-list", "--parents", "-n", "1", commit, "--")
	if err != nil {
		return "", err
	}
	parts := strings.Fields(string(out))
	if len(parts) > 1 {
		return parts[1], nil
	}
	// hash-object without -w computes the empty-tree ID without mutating Git.
	out, err = c.reviewOutput(ctx, dir, "hash-object", "-t", "tree", "--stdin")
	return strings.TrimSpace(string(out)), err
}

func (c Client) ReviewContains(ctx context.Context, dir, commit, head string) (bool, error) {
	_, err := c.reviewOutput(ctx, dir, "merge-base", "--is-ancestor", commit, head)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

type ReviewPath struct {
	Path   string
	Change string
}

func (c Client) ReviewFiles(ctx context.Context, dir, base, head string, working bool) ([]ReviewPath, bool, error) {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-status", "-z", base}
	if !working {
		args = append(args, head)
	}
	args = append(args, "--")
	out, err := c.reviewOutput(ctx, dir, args...)
	if err != nil {
		return nil, false, err
	}
	parts := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	found := map[string]string{}
	for i := 0; i+1 < len(parts); i += 2 {
		p := string(parts[i+1])
		if workspacefiles.ValidPath(p) != nil {
			continue
		}
		change := "modified"
		switch string(parts[i]) {
		case "A":
			change = "added"
		case "D":
			change = "deleted"
		case "U":
			change = "conflicted"
		}
		// An unmerged path may also have an ordinary working-file record.
		if found[p] != "conflicted" {
			found[p] = change
		}
	}
	if working {
		out, err = c.reviewOutput(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, false, err
		}
		for _, p := range bytes.Split(out, []byte{0}) {
			if workspacefiles.ValidPath(string(p)) == nil {
				if found[string(p)] == "deleted" {
					found[string(p)] = "modified"
				} else {
					found[string(p)] = "added"
				}
			}
		}
		// A comparison against a commit can report unmerged files as modified.
		// Read index stages explicitly rather than inferring conflict state.
		out, err = c.reviewOutput(ctx, dir, "ls-files", "--unmerged", "-z")
		if err != nil {
			return nil, false, err
		}
		for _, entry := range bytes.Split(out, []byte{0}) {
			_, p, ok := bytes.Cut(entry, []byte{'\t'})
			if ok && workspacefiles.ValidPath(string(p)) == nil {
				found[string(p)] = "conflicted"
			}
		}
	}
	paths := make([]string, 0, len(found))
	for p := range found {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	truncated := len(paths) > ReviewPathLimit
	if truncated {
		paths = paths[:ReviewPathLimit]
	}
	result := make([]ReviewPath, 0, len(paths))
	for _, p := range paths {
		result = append(result, ReviewPath{p, found[p]})
	}
	return result, truncated, nil
}

func (c Client) ReviewSource(ctx context.Context, dir, commit, p string) (workspacefiles.File, error) {
	if err := workspacefiles.ValidPath(p); err != nil {
		return workspacefiles.File{}, err
	}
	out, err := c.reviewOutput(ctx, dir, "ls-tree", "-z", commit, "--", p)
	if err != nil {
		return workspacefiles.File{}, err
	}
	if len(out) == 0 {
		return workspacefiles.File{Path: p}, nil
	}
	metadata, _, ok := strings.Cut(string(out), "\t")
	fields := strings.Fields(metadata)
	if !ok || len(fields) != 3 {
		return workspacefiles.File{}, errors.New("invalid Git tree entry")
	}
	if fields[0] != "100644" && fields[0] != "100755" {
		return workspacefiles.File{Path: p, Exists: true, Binary: true}, nil
	}
	size, err := c.reviewOutput(ctx, dir, "cat-file", "-s", fields[2])
	if err != nil {
		return workspacefiles.File{}, err
	}
	var n int
	if _, err = fmt.Sscanf(string(size), "%d", &n); err != nil {
		return workspacefiles.File{}, err
	}
	if n > workspacefiles.MaxBytes {
		return workspacefiles.File{Path: p, Exists: true, TooLarge: true}, nil
	}
	out, err = c.reviewOutput(ctx, dir, "cat-file", "blob", fields[2])
	if err != nil {
		return workspacefiles.File{}, err
	}
	return workspacefiles.Source(p, out), nil
}

type ReviewCommit struct {
	SHA, Subject, Author, AuthoredAt string
	Parents                          []string
}

func (c Client) ReviewCommits(ctx context.Context, dir, base, head string) ([]ReviewCommit, bool, error) {
	out, err := c.reviewOutput(ctx, dir, "log", "-101", "--format=%H%x00%s%x00%an%x00%aI%x00", base+".."+head, "--")
	if err != nil {
		return nil, false, err
	}
	fields := strings.Split(string(out), "\x00")
	result := []ReviewCommit{}
	for len(fields) >= 4 {
		result = append(result, ReviewCommit{SHA: strings.TrimSpace(fields[0]), Subject: fields[1], Author: fields[2], AuthoredAt: fields[3]})
		fields = fields[4:]
	}
	truncated := len(result) > 100
	if truncated {
		result = result[:100]
	}
	return result, truncated, nil
}
