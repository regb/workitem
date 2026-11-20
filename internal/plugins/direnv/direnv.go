// Package direnv supplies the bundled environment plugin. All direnv
// commands and formats belong here, not in workspace/agent core.
package direnv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	adapter "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/options"
	"github.com/regb/workitem/internal/plugin"
)

func Definition() options.Definition {
	// trust is retired: a registered repository's .envrc is loaded without
	// asking, like the rest of its code its agents run.
	return options.Definition{Name: "direnv", RetiredSections: []string{"direnv"}, RetiredOptions: []string{"trust"}, Options: []options.Option{
		{Name: "load", Type: "bool", Default: true, Repository: true, Description: "Load the worktree's .envrc into agents and the commands wi runs there. Inherited-state cleanup remains enabled."},
	}}
}

type Client interface {
	Status(context.Context, string) (adapter.Status, error)
	// Allow allows an .envrc for direnv run with the given environment.
	Allow(context.Context, string, map[string]string) error
	Environment(context.Context, string, map[string]string) (map[string]string, error)
}

func New(client Client) plugin.Hooks {
	d := &implementation{client: client}
	return plugin.Hooks{Name: "direnv", Sanitize: d.sanitize, CommandEnvironment: d.command, RuntimeEnvironment: d.runtime}
}

type implementation struct {
	client Client
}

func enabled(settings map[string]any) bool { load, ok := settings["load"].(bool); return !ok || load }
func absent(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file or directory"))
}
func managed(request plugin.EnvironmentRequest) map[string]bool {
	result := adapter.ManagedVariables(request.Values["DIRENV_DIFF"])
	for key := range adapter.ManagedVariables(request.Source["DIRENV_DIFF"]) {
		result[key] = true
	}
	return result
}
func (d *implementation) sanitize(ctx context.Context, r plugin.EnvironmentRequest, _ map[string]any) (plugin.Patch, error) {
	patch := plugin.Patch{}
	owned := managed(r)
	for key := range r.Values {
		if strings.HasPrefix(key, "DIRENV_") || (owned[key] && key != "PATH" && key != "HOME" && !shellVariable(key)) {
			patch.Unset = append(patch.Unset, key)
		}
	}
	return patch, nil
}
func shellVariable(key string) bool {
	switch key {
	case "PWD", "OLDPWD", "SHLVL", "_", "PROMPT_COMMAND", "TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "VTE_VERSION", "SHELL":
		return true
	}
	return false
}

// Only act on the checkout's own regular .envrc. Never approve or revoke an
// ancestor file (or a symlink escaping the verified worktree).
func contained(root, path string) bool {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) || filepath.Base(path) != ".envrc" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	canonical, err := filepath.EvalSymlinks(root)
	return err == nil && resolved == filepath.Join(canonical, ".envrc")
}
func (d *implementation) runtime(ctx context.Context, r plugin.EnvironmentRequest, settings map[string]any) (plugin.Patch, error) {
	patch := plugin.Patch{}
	if d.client == nil || !enabled(settings) || r.Subject.CheckoutPath == "" {
		return patch, nil
	}
	// direnv records an allowance under the file's resolved path but checks
	// the path it is given, so a checkout reached through a symlink (such
	// as /tmp on macOS) is inspected, allowed and loaded resolved.
	checkout := canonical(r.Subject.CheckoutPath)
	status, err := d.client.Status(ctx, checkout)
	if err != nil {
		if absent(err) {
			return patch, nil
		}
		patch.Warnings = append(patch.Warnings, "could not inspect worktree direnv environment: "+err.Error())
		return patch, nil
	}
	if !status.Found {
		return patch, nil
	}
	if !contained(r.Subject.CheckoutPath, status.RCPath) {
		return patch, fmt.Errorf("direnv .envrc is outside the verified checkout or is not a regular local file")
	}
	if !status.Allowed {
		// Registering a repository lets its agents run its code; its
		// worktree's .envrc is part of that. Allow it with the environment
		// the agent loads it with, which decides where direnv keeps it.
		if err := d.client.Allow(ctx, canonical(status.RCPath), r.Values); err != nil {
			return patch, fmt.Errorf("allow worktree .envrc: %w", err)
		}
	}
	exported, err := d.client.Environment(ctx, checkout, r.Values)
	if err != nil {
		return patch, fmt.Errorf("load worktree .envrc: %w", err)
	}
	patch.Set = map[string]string{}
	for key, value := range exported {
		if !strings.HasPrefix(key, "DIRENV_") {
			patch.Set[key] = value
		}
	}
	return patch, nil
}
func (d *implementation) command(ctx context.Context, r plugin.EnvironmentRequest, settings map[string]any) (plugin.Patch, error) {
	patch := plugin.Patch{Set: map[string]string{}}
	owned := managed(r)
	for _, key := range r.AllowedKeys {
		if owned[key] {
			patch.Unset = append(patch.Unset, key)
		}
	}
	if d.client == nil || !enabled(settings) || r.Subject.CheckoutPath == "" {
		return patch, nil
	}
	status, err := d.client.Status(ctx, canonical(r.Subject.CheckoutPath))
	if err != nil {
		if absent(err) {
			return patch, nil
		}
		return patch, fmt.Errorf("inspect current direnv environment: %w", err)
	}
	if !status.Found || !status.Allowed {
		return patch, nil
	}
	boundary := r.Subject.BoundaryPath
	if boundary == "" {
		boundary = filepath.Dir(status.RCPath)
	}
	canonicalBoundary, err := filepath.EvalSymlinks(boundary)
	if err != nil {
		return patch, fmt.Errorf("inspect current environment boundary")
	}
	canonicalCWD, err := filepath.EvalSymlinks(r.Subject.CheckoutPath)
	if err != nil {
		return patch, fmt.Errorf("inspect current environment directory")
	}
	rel, err := filepath.Rel(canonicalBoundary, canonicalCWD)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !localRCWithin(boundary, status.RCPath) {
		return patch, fmt.Errorf("direnv .envrc is outside the current checkout")
	}
	base := map[string]string{}
	for key, value := range r.Values {
		if !strings.HasPrefix(key, "DIRENV_") && (!owned[key] || key == "PATH" || key == "HOME") {
			base[key] = value
		}
	}
	exported, err := d.client.Environment(ctx, canonical(r.Subject.CheckoutPath), base)
	if err != nil {
		return patch, fmt.Errorf("load current direnv environment: %w", err)
	}
	for _, key := range r.AllowedKeys {
		if _, explicit := r.Source[key]; explicit && !owned[key] {
			continue
		}
		if value, ok := exported[key]; ok {
			patch.Set[key] = value
		}
	}
	return patch, nil
}

// canonical resolves symlinks in a path, or returns it as given.
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
