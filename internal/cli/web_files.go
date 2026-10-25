package cli

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/regb/workitem/internal/fzf"
	gitpkg "github.com/regb/workitem/internal/git"
)

// mentionLimit bounds the paths an @-mention lists.
const mentionLimit = 50

// findFiles fuzzy-finds paths in a worktree for an @-mention: its files,
// tracked or untracked but not ignored, and their folders.
func findFiles(ctx context.Context, dir, query string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	listed, err := gitpkg.New("git").ListFiles(ctx, dir)
	if err != nil {
		return nil, errors.New("could not list the worktree's files")
	}
	return rankMentionPaths(ctx, listed, query)
}

// findCommitFiles fuzzy-finds paths in a commit of a bare repository, for
// work that has no worktree yet: new work, starting from the integration
// branch.
func findCommitFiles(ctx context.Context, gitDir, commit, query string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	listed, err := gitpkg.New("git").ListCommitFiles(ctx, gitDir, commit)
	if err != nil {
		return nil, errors.New("could not list the project's files")
	}
	return rankMentionPaths(ctx, listed, query)
}

// rankMentionPaths ranks paths and their folders (with a trailing slash)
// against query with fzf. An empty query lists the shallowest paths.
func rankMentionPaths(ctx context.Context, paths []string, query string) ([]string, error) {
	candidates := pathsWithFolders(paths)
	if strings.TrimSpace(query) == "" {
		sort.SliceStable(candidates, func(i, j int) bool { return pathDepth(candidates[i]) < pathDepth(candidates[j]) })
		return candidates[:min(mentionLimit, len(candidates))], nil
	}
	matches, err := fzf.Filter(ctx, candidates, query, mentionLimit)
	if err != nil {
		return nil, errors.New("could not search files; is fzf installed?")
	}
	return matches, nil
}

// pathsWithFolders adds each path's folders to the paths, once.
func pathsWithFolders(paths []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, file := range paths {
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		out = append(out, file)
		for dir := path.Dir(file); dir != "." && dir != "/" && !seen[dir+"/"]; dir = path.Dir(dir) {
			seen[dir+"/"] = true
			out = append(out, dir+"/")
		}
	}
	return out
}

func pathDepth(file string) int { return strings.Count(strings.TrimSuffix(file, "/"), "/") }
