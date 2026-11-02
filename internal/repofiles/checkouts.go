package repofiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	gitpkg "github.com/regb/workitem/internal/git"
)

// A checkout's copies of repository files are recorded in its own Git
// directory, so a later claim, or a removal, knows which files Wi placed.
const copiesRecord = "wi-files.json"

type copies struct {
	ClaimID string            `json:"claim_id"`
	Files   map[string]string `json:"files"`
}

// CopyInto copies the stored files into a checkout for a new claim, except
// skipped ones and ones the branch tracks, and removes unedited copies of
// files no longer copied. It returns warnings for what it left alone.
func CopyInto(ctx context.Context, checkout, filesDir, claimID string, skip map[string]bool, log io.Writer) ([]string, error) {
	stored, err := List(filesDir)
	if err != nil {
		return nil, fmt.Errorf("read repository files: %w", err)
	}
	gitDir, previous, err := readCopies(checkout)
	if err != nil {
		return nil, err
	}
	candidates := []string{}
	for _, file := range stored {
		candidates = append(candidates, file.Path)
	}
	for p := range previous.Files {
		candidates = append(candidates, p)
	}
	tracked, err := trackedPaths(ctx, checkout, candidates)
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	placed := copies{ClaimID: claimID, Files: map[string]string{}}
	for _, file := range stored {
		switch {
		case skip[file.Path]:
			continue
		case tracked[file.Path]:
			warnings = append(warnings, fmt.Sprintf("wi file %s is shadowed by the repository's tracked %s; not copied", file.Path, file.Path))
			continue
		}
		content, err := Read(filesDir, file.Path)
		if err != nil {
			return warnings, err
		}
		if err := place(checkout, file.Path, content); err != nil {
			return warnings, fmt.Errorf("copy %s: %w", file.Path, err)
		}
		placed.Files[file.Path] = Digest(content)
		fmt.Fprintf(log, "copied %s\n", file.Path)
	}
	for p, digest := range previous.Files {
		if _, kept := placed.Files[p]; kept || tracked[p] {
			continue
		}
		warning, err := dropCopy(checkout, p, digest, log)
		if err != nil {
			return warnings, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	return warnings, writeCopies(gitDir, placed)
}

// Sync brings a repository's checkouts in line with its stored files after
// one is added or removed: checkouts not in use lose unedited copies of
// removed files at once, others at their next claim, and info/exclude lists
// what Git must ignore meanwhile.
func Sync(ctx context.Context, root, filesDir string, inUse map[string]bool) ([]string, error) {
	stored, err := List(filesDir)
	if err != nil {
		return nil, fmt.Errorf("read repository files: %w", err)
	}
	keep := map[string]bool{}
	for _, file := range stored {
		keep[file.Path] = true
	}
	checkouts, err := Worktrees(ctx, root)
	if err != nil {
		return nil, err
	}
	// Git reports resolved paths; claims may name them through a symlink.
	busy := map[string]bool{}
	for path := range inUse {
		busy[resolved(path)] = true
	}
	warnings := []string{}
	for _, checkout := range checkouts {
		if busy[resolved(checkout)] {
			continue
		}
		gitDir, previous, err := readCopies(checkout)
		if err != nil || len(previous.Files) == 0 {
			continue
		}
		tracked, err := trackedPaths(ctx, checkout, slices.Collect(maps.Keys(previous.Files)))
		if err != nil {
			return warnings, err
		}
		for p, digest := range previous.Files {
			if keep[p] || tracked[p] {
				continue
			}
			warning, err := dropCopy(checkout, p, digest, io.Discard)
			if err != nil {
				return warnings, err
			}
			if warning != "" {
				warnings = append(warnings, warning)
			}
			delete(previous.Files, p)
		}
		if err := writeCopies(gitDir, previous); err != nil {
			return warnings, err
		}
	}
	return warnings, UpdateExclude(ctx, root, filesDir)
}

// UpdateExclude rewrites Wi's section of the store's info/exclude, which
// every worktree reads: the stored files, and copies still present in any
// checkout, so Git ignores them.
func UpdateExclude(ctx context.Context, root, filesDir string) error {
	stored, err := List(filesDir)
	if err != nil {
		return fmt.Errorf("read repository files: %w", err)
	}
	paths := map[string]bool{}
	for _, file := range stored {
		paths[file.Path] = true
	}
	checkouts, err := Worktrees(ctx, root)
	if err != nil {
		return err
	}
	for _, checkout := range checkouts {
		if _, previous, err := readCopies(checkout); err == nil {
			for p := range previous.Files {
				if _, err := os.Lstat(filepath.Join(checkout, filepath.FromSlash(p))); err == nil {
					paths[p] = true
				}
			}
		}
	}
	listed := slices.Sorted(maps.Keys(paths))
	if err := writeExclude(root, listed); err != nil {
		return fmt.Errorf("update info/exclude: %w", err)
	}
	return nil
}

// Worktrees lists the linked worktrees of a bare store.
func Worktrees(ctx context.Context, root string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "--git-dir", root, "worktree", "list", "--porcelain")
	command.Env = gitpkg.RepositoryEnvironment()
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	checkouts := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if info, err := os.Stat(filepath.Join(path, ".git")); err == nil && !info.IsDir() {
			checkouts = append(checkouts, path)
		}
	}
	return checkouts, nil
}

// dropCopy removes a copy Wi placed unless it was edited since, which it
// reports instead.
func dropCopy(checkout, p, digest string, log io.Writer) (string, error) {
	full := filepath.Join(checkout, filepath.FromSlash(p))
	content, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil || Digest(content) != digest {
		return fmt.Sprintf("left %s in %s: it is no longer a copied wi file but was edited", p, filepath.Base(checkout)), nil
	}
	if err := os.Remove(full); err != nil {
		return "", err
	}
	fmt.Fprintf(log, "removed %s\n", p)
	return "", nil
}

// readCopies reads which copies Wi placed in a checkout.
func readCopies(checkout string) (string, copies, error) {
	record := copies{Files: map[string]string{}}
	gitDir, err := checkoutGitDir(checkout)
	if err != nil {
		return "", record, err
	}
	if content, err := os.ReadFile(filepath.Join(gitDir, copiesRecord)); err == nil {
		_ = json.Unmarshal(content, &record)
	}
	if record.Files == nil {
		record.Files = map[string]string{}
	}
	return gitDir, record, nil
}

func writeCopies(gitDir string, record copies) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return WriteFile(filepath.Join(gitDir, copiesRecord), encoded)
}

// place writes content at p inside checkout, refusing to follow a symlink.
func place(checkout, p string, content []byte) error {
	if err := ValidPath(p); err != nil {
		return err
	}
	parts := strings.Split(p, "/")
	dir := checkout
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
	}
	full := filepath.Join(checkout, filepath.FromSlash(p))
	if info, err := os.Lstat(full); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file", p)
	}
	return WriteFile(full, content)
}

// trackedPaths reports which of paths Git tracks in the checkout.
func trackedPaths(ctx context.Context, checkout string, paths []string) (map[string]bool, error) {
	tracked := map[string]bool{}
	if len(paths) == 0 {
		return tracked, nil
	}
	command := exec.CommandContext(ctx, "git", append([]string{"-C", checkout, "ls-files", "-z", "--"}, paths...)...)
	command.Env = gitpkg.RepositoryEnvironment()
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	for _, p := range strings.Split(string(output), "\x00") {
		if p != "" {
			tracked[p] = true
		}
	}
	return tracked, nil
}

// checkoutGitDir is a linked worktree's own Git directory.
func checkoutGitDir(checkout string) (string, error) {
	content, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		return "", fmt.Errorf("read checkout .git: %w", err)
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir: ")
	if !ok {
		return "", errors.New("checkout .git is not a linked worktree")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(checkout, dir)
	}
	return dir, nil
}

const (
	excludeBegin = "# wi files: managed by wi, do not edit"
	excludeEnd   = "# end of wi files"
)

// writeExclude keeps Wi's section of the store's info/exclude listing
// exactly paths, leaving the rest of the file alone.
func writeExclude(root string, paths []string) error {
	path := filepath.Join(root, "info", "exclude")
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	kept := []string{}
	inside := false
	for _, line := range strings.Split(strings.TrimRight(string(current), "\n"), "\n") {
		switch {
		case line == excludeBegin:
			inside = true
		case line == excludeEnd:
			inside = false
		case !inside && (line != "" || len(kept) > 0):
			kept = append(kept, line)
		}
	}
	lines := append([]string(nil), kept...)
	if len(paths) > 0 {
		lines = append(lines, excludeBegin)
		for _, p := range paths {
			lines = append(lines, "/"+escapePattern(p))
		}
		lines = append(lines, excludeEnd)
	}
	next := strings.Join(lines, "\n")
	if next != "" {
		next += "\n"
	}
	if next == string(current) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return WriteFile(path, []byte(next))
}

// escapePattern makes a path match itself literally in a gitignore file.
func escapePattern(p string) string {
	var b bytes.Buffer
	for i, r := range p {
		if strings.ContainsRune(`*?[]\`, r) || (i == 0 && (r == '!' || r == '#')) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func resolved(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
