// Preparation hooks are contributions in the common plugin catalog. This pipeline does not
// start runtimes, select work, write structured state, or load plugins.
package plugin

import (
	"context"
	"fmt"
	"io"
	"maps"
	"sort"
	"strings"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/options"
)

type Subject struct {
	ItemID, RepositoryID, CheckoutPath, BoundaryPath, ClaimID, Mode string
	Generation                                                      uint64
}
type Approval struct{ ItemID, Path, Question string }
type Approver func(context.Context, Approval) (bool, error)

type approverKey struct{}

// WithApprover answers approvals for work done under ctx, ahead of the
// process's own approver: a web request asks its browser, never the terminal
// the server happens to run in.
func WithApprover(ctx context.Context, approve Approver) context.Context {
	return context.WithValue(ctx, approverKey{}, approve)
}

// ContextApprover returns the approver set by WithApprover, or nil.
func ContextApprover(ctx context.Context) Approver {
	approve, _ := ctx.Value(approverKey{}).(Approver)
	return approve
}

// ApprovalRequired is returned by an approver that cannot ask now, such as
// a web request whose browser has not answered yet. The caller asks and
// retries with the answer.
type ApprovalRequired struct{ Approval Approval }

func (e *ApprovalRequired) Error() string {
	return fmt.Sprintf("%s %s", e.Approval.Question, e.Approval.Path)
}

type EnvironmentRequest struct {
	Subject        Subject
	Values, Source map[string]string
	AllowedKeys    []string
}
type Patch struct {
	Set      map[string]string
	Unset    []string
	Warnings []string
}

// SetupRequest is a published claim being made ready for work, after the
// checkout is claimed and before an agent starts in it. Hooks share its
// environment and log; each run belongs to one claim generation.
type SetupRequest struct {
	Subject Subject
	// Slot names the managed slot; Reused says another claim used it before.
	Slot   string
	Reused bool
	// RepositoryRoot is the owned bare store; DefaultBranch its integration
	// branch, which holds the repository's checked-in configuration.
	RepositoryRoot, DefaultBranch string
	// FilesDir holds the repository's files (internal/repofiles). Hooks read it.
	FilesDir string
	// Environment is the base environment for processes a hook runs, with
	// the run's WI_* variables. Hooks never change it.
	Environment map[string]string
	// Log receives what hooks and their processes print.
	Log io.Writer
}

type Hooks struct {
	Name               string
	Sanitize           func(context.Context, EnvironmentRequest, map[string]any) (Patch, error)
	CommandEnvironment func(context.Context, EnvironmentRequest, map[string]any) (Patch, error)
	RuntimeEnvironment func(context.Context, EnvironmentRequest, map[string]any) (Patch, error)
	PrepareCheckout    func(context.Context, contract.CheckoutPreparation, map[string]any) ([]string, error)
	// SetupCheckout runs after claim publication, in catalog order; an error
	// stops the run and keeps the agent from starting.
	SetupCheckout func(context.Context, SetupRequest, map[string]any) ([]string, error)
	// SetupOrder places the hook among SetupCheckout hooks, lowest first.
	SetupOrder int
}
type Pipeline struct {
	Hooks   []Hooks
	Options func(context.Context, string, string) (map[string]any, error)
}

func (p Pipeline) settings(ctx context.Context, repo, name string) (map[string]any, error) {
	if p.Options == nil {
		return map[string]any{}, nil
	}
	v, err := p.Options(ctx, repo, name)
	if err != nil {
		return nil, err
	}
	return options.CloneValues(v), nil
}
func (p Pipeline) check() error {
	names := map[string]bool{}
	for _, h := range p.Hooks {
		if h.Name == "" || names[h.Name] {
			return fmt.Errorf("duplicate or unnamed extension hook")
		}
		names[h.Name] = true
	}
	return nil
}
func (p Pipeline) Environment(ctx context.Context, phase string, request EnvironmentRequest) (map[string]string, []string, []string, error) {
	if phase != "sanitize" && phase != "command" && phase != "runtime" {
		return nil, nil, nil, fmt.Errorf("unknown environment phase")
	}
	values := maps.Clone(request.Values)
	if values == nil {
		values = map[string]string{}
	}
	removed := map[string]bool{}
	warnings := []string{}
	if err := p.check(); err != nil {
		return nil, nil, warnings, err
	}
	for _, h := range p.Hooks {
		if err := ctx.Err(); err != nil {
			return nil, nil, warnings, err
		}
		fn := h.RuntimeEnvironment
		switch phase {
		case "sanitize":
			fn = h.Sanitize
		case "command":
			fn = h.CommandEnvironment
		case "runtime":
		default:
			return nil, nil, warnings, fmt.Errorf("unknown environment phase")
		}
		if fn == nil {
			continue
		}
		settings, err := p.settings(ctx, request.Subject.RepositoryID, h.Name)
		if err != nil {
			return nil, nil, warnings, err
		}
		input := request
		input.Values = maps.Clone(values)
		input.Source = maps.Clone(request.Source)
		input.AllowedKeys = append([]string(nil), request.AllowedKeys...)
		patch, err := fn(ctx, input, settings)
		if err == nil {
			err = ctx.Err()
		}
		warnings = append(warnings, patch.Warnings...)
		if err != nil {
			return nil, nil, warnings, fmt.Errorf("extension %s: %w", h.Name, err)
		}
		if phase == "sanitize" && len(patch.Set) > 0 {
			return nil, nil, warnings, fmt.Errorf("extension %s sanitizer may only remove environment values", h.Name)
		}
		allowed := func(key string) bool {
			if phase == "sanitize" {
				return true
			}
			if phase == "command" {
				for _, k := range request.AllowedKeys {
					if key == k {
						return true
					}
				}
				return false
			}
			return key != "TMUX" && key != "TMUX_PANE" && (!strings.HasPrefix(key, "WI_") || key == "WI_LIST_LABELS" || key == "WI_ITEM_DEFAULT_LABELS")
		}
		for _, key := range patch.Unset {
			if !allowed(key) {
				return nil, nil, warnings, fmt.Errorf("extension %s attempted to remove a protected environment variable", h.Name)
			}
			delete(values, key)
			removed[key] = true
		}
		for key, value := range patch.Set {
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
				return nil, nil, warnings, fmt.Errorf("extension %s returned an invalid environment entry", h.Name)
			}
			if !allowed(key) {
				if key == "TMUX" || key == "TMUX_PANE" {
					continue
				}
				warnings = append(warnings, "ignored reserved extension variable "+key)
				continue
			}
			values[key] = value
			delete(removed, key)
		}
	}
	keys := make([]string, 0, len(removed))
	for key := range removed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return values, keys, warnings, nil
}
func (p Pipeline) PrepareCheckout(ctx context.Context, request contract.CheckoutPreparation) ([]string, error) {
	warnings := []string{}
	if err := p.check(); err != nil {
		return warnings, err
	}
	for _, h := range p.Hooks {
		if err := ctx.Err(); err != nil {
			return warnings, err
		}
		if h.PrepareCheckout == nil {
			continue
		}
		settings, err := p.settings(ctx, request.RepositoryID, h.Name)
		if err != nil {
			return warnings, err
		}
		w, err := h.PrepareCheckout(ctx, request, settings)
		if err == nil {
			err = ctx.Err()
		}
		warnings = append(warnings, w...)
		if err != nil {
			return warnings, fmt.Errorf("extension %s checkout preparation: %w", h.Name, err)
		}
	}
	return warnings, nil
}

// SetupCheckout runs every SetupCheckout hook in SetupOrder, stopping at the
// first error.
func (p Pipeline) SetupCheckout(ctx context.Context, request SetupRequest) ([]string, error) {
	warnings := []string{}
	if err := p.check(); err != nil {
		return warnings, err
	}
	hooks := append([]Hooks(nil), p.Hooks...)
	sort.SliceStable(hooks, func(i, j int) bool { return hooks[i].SetupOrder < hooks[j].SetupOrder })
	for _, h := range hooks {
		if err := ctx.Err(); err != nil {
			return warnings, err
		}
		if h.SetupCheckout == nil {
			continue
		}
		settings, err := p.settings(ctx, request.Subject.RepositoryID, h.Name)
		if err != nil {
			return warnings, err
		}
		w, err := h.SetupCheckout(ctx, request, settings)
		if err == nil {
			err = ctx.Err()
		}
		warnings = append(warnings, w...)
		if err != nil {
			return warnings, fmt.Errorf("%s: %w", h.Name, err)
		}
	}
	return warnings, nil
}

// RunShell runs a shell command in dir as its own process group, which is
// killed when ctx ends, with exactly env.
func RunShell(ctx context.Context, name, dir, command string, env map[string]string, output io.Writer) error {
	entries := make([]string, 0, len(env))
	for key, value := range env {
		entries = append(entries, key+"="+value)
	}
	sort.Strings(entries)
	return RunCommand(ctx, Spec{Name: name, Command: []string{"/bin/sh", "-c", command}}, nil, entries, dir, nil, output, output)
}
