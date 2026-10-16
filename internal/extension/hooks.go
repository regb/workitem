// Package extension defines synchronous host preparation ports. It does not
// start runtimes, select work, write structured state, or load plugins.
package extension

import (
	"context"
	"fmt"
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
type Hooks struct {
	Name               string
	Sanitize           func(context.Context, EnvironmentRequest, map[string]any) (Patch, error)
	CommandEnvironment func(context.Context, EnvironmentRequest, map[string]any) (Patch, error)
	RuntimeEnvironment func(context.Context, EnvironmentRequest, map[string]any) (Patch, error)
	PrepareCheckout    func(context.Context, contract.CheckoutPreparation, map[string]any) ([]string, error)
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
