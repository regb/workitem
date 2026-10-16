// Package direnv supplies the bundled environment extension. All direnv
// commands, formats, and trust policy belong here, not in workspace/agent core.
package direnv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/app/contract"
	adapter "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/extension"
	"github.com/regb/workitem/internal/options"
)

func Definition() options.Definition {
	return options.Definition{Name: "direnv", RetiredSections: []string{"direnv"}, Options: []options.Option{
		{Name: "load", Type: "bool", Default: true, Repository: true, Description: "Load approved project environments. Inherited-state cleanup remains enabled."},
		{Name: "trust", Type: "string", Default: "prompt", Choices: []string{"prompt", "always", "never"}, Repository: true, Description: "Approval policy for runtime startup; user configuration only."},
	}}
}

type Client interface {
	Status(context.Context, string) (adapter.Status, error)
	Allow(context.Context, string) error
	Deny(context.Context, string) error
	Environment(context.Context, string, map[string]string) (map[string]string, error)
}

func New(client Client, approve extension.Approver) extension.Hooks {
	d := &implementation{client: client, approve: approve}
	return extension.Hooks{Name: "direnv", Sanitize: d.sanitize, CommandEnvironment: d.command, RuntimeEnvironment: d.runtime, PrepareCheckout: d.checkout}
}

type implementation struct {
	client  Client
	approve extension.Approver
}

func enabled(settings map[string]any) bool { load, ok := settings["load"].(bool); return !ok || load }
func absent(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file or directory"))
}
func managed(request extension.EnvironmentRequest) map[string]bool {
	result := adapter.ManagedVariables(request.Values["DIRENV_DIFF"])
	for key := range adapter.ManagedVariables(request.Source["DIRENV_DIFF"]) {
		result[key] = true
	}
	return result
}
func (d *implementation) sanitize(ctx context.Context, r extension.EnvironmentRequest, _ map[string]any) (extension.Patch, error) {
	patch := extension.Patch{}
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
func (d *implementation) runtime(ctx context.Context, r extension.EnvironmentRequest, settings map[string]any) (extension.Patch, error) {
	patch := extension.Patch{}
	if d.client == nil || !enabled(settings) || r.Subject.CheckoutPath == "" {
		return patch, nil
	}
	status, err := d.client.Status(ctx, r.Subject.CheckoutPath)
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
		before, err := snapshot(status.RCPath)
		if err != nil {
			return patch, err
		}
		policy, _ := settings["trust"].(string)
		approved := policy == "always"
		if !approved && policy != "never" && d.approve != nil {
			approved, err = d.approve(ctx, extension.Approval{ItemID: r.Subject.ItemID, Path: status.RCPath, Question: "Allow worktree .envrc?"})
			if err != nil {
				return patch, fmt.Errorf("request direnv approval: %w", err)
			}
		}
		if !approved {
			patch.Warnings = append(patch.Warnings, fmt.Sprintf("worktree .envrc is not trusted; agent runtime will start without it (run `direnv allow %s` to trust it)", status.RCPath))
			return patch, nil
		}
		after, err := snapshot(status.RCPath)
		if err != nil || before != after || !contained(r.Subject.CheckoutPath, status.RCPath) {
			return patch, fmt.Errorf("worktree .envrc changed while approval was pending; retry")
		}
		if err := d.client.Allow(ctx, status.RCPath); err != nil {
			return patch, fmt.Errorf("allow worktree .envrc: %w", err)
		}
		if policy == "always" {
			patch.Warnings = append(patch.Warnings, "auto-trusted worktree .envrc by operator extension configuration")
		} else {
			patch.Warnings = append(patch.Warnings, "trusted worktree .envrc after operator approval")
		}
	}
	exported, err := d.client.Environment(ctx, r.Subject.CheckoutPath, r.Values)
	if err != nil {
		return patch, fmt.Errorf("load trusted worktree .envrc: %w", err)
	}
	patch.Set = map[string]string{}
	for key, value := range exported {
		if !strings.HasPrefix(key, "DIRENV_") {
			patch.Set[key] = value
		}
	}
	return patch, nil
}
func (d *implementation) checkout(ctx context.Context, r contract.CheckoutPreparation, _ map[string]any) ([]string, error) {
	if d.client == nil || !r.Reused {
		return nil, nil
	}
	status, err := d.client.Status(ctx, r.Path)
	if err != nil {
		if absent(err) {
			return nil, nil
		}
		return nil, err
	}
	if !status.Found || !status.Allowed {
		return nil, nil
	}
	if !contained(r.Path, status.RCPath) {
		return nil, fmt.Errorf("refusing to revoke direnv trust outside the reserved checkout")
	}
	if err := d.client.Deny(ctx, status.RCPath); err != nil {
		return nil, err
	}
	return []string{"revoked prior direnv trust for reused slot; runtime startup will request approval again"}, nil
}
func (d *implementation) command(ctx context.Context, r extension.EnvironmentRequest, settings map[string]any) (extension.Patch, error) {
	patch := extension.Patch{Set: map[string]string{}}
	owned := managed(r)
	for _, key := range r.AllowedKeys {
		if owned[key] {
			patch.Unset = append(patch.Unset, key)
		}
	}
	if d.client == nil || !enabled(settings) || r.Subject.CheckoutPath == "" {
		return patch, nil
	}
	status, err := d.client.Status(ctx, r.Subject.CheckoutPath)
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
	exported, err := d.client.Environment(ctx, r.Subject.CheckoutPath, base)
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
