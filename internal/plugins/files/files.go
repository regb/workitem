// Package files supplies the bundled plugin that copies a repository's files
// (internal/repofiles) into each claimed checkout before setup, and keeps Git
// ignoring them through the owned store's info/exclude.
package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/regb/workitem/internal/options"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/repofiles"
)

// Order runs file copies before setup commands, which may read the files.
const Order = 10

func Definition() options.Definition {
	return options.Definition{Name: "files", Options: []options.Option{
		{Name: "skip", Type: "strings", Default: []string{}, Repository: true, Description: "Repository files not copied into checkouts; setup still reads them from WI_FILES."},
	}}
}

func New() plugin.Hooks {
	return plugin.Hooks{Name: "files", SetupCheckout: setup, SetupOrder: Order}
}

// record lists what was copied into a checkout, kept in its Git directory so
// a later claim of the slot can remove copies of files since removed.
type record struct {
	ClaimID string            `json:"claim_id"`
	Files   map[string]string `json:"files"`
}

const recordName = "wi-files.json"

func setup(ctx context.Context, request plugin.SetupRequest, settings map[string]any) ([]string, error) {
	checkout := request.Subject.CheckoutPath
	stored, err := repofiles.List(request.FilesDir)
	if err != nil {
		return nil, fmt.Errorf("read repository files: %w", err)
	}
	paths := make([]string, 0, len(stored))
	for _, file := range stored {
		paths = append(paths, file.Path)
	}
	if err := writeExclude(request.RepositoryRoot, paths); err != nil {
		return nil, fmt.Errorf("update info/exclude: %w", err)
	}
	gitDir, err := checkoutGitDir(checkout)
	if err != nil {
		return nil, err
	}
	previous := readRecord(filepath.Join(gitDir, recordName))
	skip := map[string]bool{}
	for _, p := range stringsSetting(settings["skip"]) {
		skip[p] = true
	}
	candidates := slices.Clone(paths)
	for p := range previous.Files {
		candidates = append(candidates, p)
	}
	tracked, err := trackedPaths(ctx, checkout, candidates)
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	copied := record{ClaimID: request.Subject.ClaimID, Files: map[string]string{}}
	for _, file := range stored {
		switch {
		case skip[file.Path]:
			continue
		case tracked[file.Path]:
			warnings = append(warnings, fmt.Sprintf("wi file %s is shadowed by the repository's tracked %s; not copied", file.Path, file.Path))
			continue
		}
		content, err := repofiles.Read(request.FilesDir, file.Path)
		if err != nil {
			return warnings, err
		}
		if err := place(checkout, file.Path, content); err != nil {
			return warnings, fmt.Errorf("copy %s: %w", file.Path, err)
		}
		copied.Files[file.Path] = repofiles.Digest(content)
		fmt.Fprintf(request.Log, "copied %s\n", file.Path)
	}
	// Copies of files no longer copied go, unless edited since.
	for p, digest := range previous.Files {
		if _, kept := copied.Files[p]; kept || tracked[p] {
			continue
		}
		full := filepath.Join(checkout, filepath.FromSlash(p))
		content, err := os.ReadFile(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || repofiles.Digest(content) != digest {
			warnings = append(warnings, fmt.Sprintf("left %s in the checkout: it is no longer a copied wi file but was edited", p))
			continue
		}
		if err := os.Remove(full); err != nil {
			return warnings, err
		}
		fmt.Fprintf(request.Log, "removed %s\n", p)
	}
	encoded, _ := json.Marshal(copied)
	if err := repofiles.WriteFile(filepath.Join(gitDir, recordName), encoded); err != nil {
		return warnings, err
	}
	return warnings, nil
}

// place writes content at p inside checkout, refusing to follow a symlink.
func place(checkout, p string, content []byte) error {
	if err := repofiles.ValidPath(p); err != nil {
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
	return repofiles.WriteFile(full, content)
}

// trackedPaths reports which of paths Git tracks in the checkout.
func trackedPaths(ctx context.Context, checkout string, paths []string) (map[string]bool, error) {
	tracked := map[string]bool{}
	if len(paths) == 0 {
		return tracked, nil
	}
	command := exec.CommandContext(ctx, "git", append([]string{"-C", checkout, "ls-files", "-z", "--"}, paths...)...)
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
	line := strings.TrimSpace(string(content))
	dir, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok {
		return "", errors.New("checkout .git is not a linked worktree")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(checkout, dir)
	}
	return dir, nil
}

func readRecord(path string) record {
	var r record
	if content, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(content, &r)
	}
	return r
}

const (
	excludeBegin = "# wi files: managed by wi, do not edit"
	excludeEnd   = "# end of wi files"
)

// writeExclude keeps wi's section of the store's info/exclude, which every
// worktree reads, listing exactly the repository's files.
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
	return repofiles.WriteFile(path, []byte(next))
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

func stringsSetting(value any) []string {
	switch v := value.(type) {
	case []string:
		return slices.Clone(v)
	case []any:
		result := []string{}
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}
