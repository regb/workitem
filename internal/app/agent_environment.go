package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/regb/workitem/internal/agent"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
)

func exactRuntimeEnvironment(environment map[string]string) []string {
	return agentcore.MergeEnvironment(nil, environment)
}

// baseRuntimeEnvironment selects the caller/server base, then lets registered
// sanitizers remove inherited project values before enforcing host identity.
func (a *App) baseRuntimeEnvironment(ctx context.Context, mode agent.Mode, subject plugin.Subject) (map[string]string, []string, []string, error) {
	var global map[string]string
	if mode == agent.ModeTUI && a.Tmux != nil {
		if candidate, err := a.Tmux.GlobalEnvironment(ctx); err == nil && len(candidate) > 0 {
			global = candidate
		}
	}
	if len(global) == 0 {
		global = envFromSlice(os.Environ())
	}
	global, scrub, warnings, err := a.Preparation.Environment(ctx, "sanitize", plugin.EnvironmentRequest{Subject: subject, Values: global, Source: envFromSlice(os.Environ())})
	if err != nil {
		return nil, nil, warnings, err
	}
	for key := range global {
		if key == "TMUX" || key == "TMUX_PANE" || key == "PI_SESSION_ID" || key == "PI_CODING_AGENT_SESSION_DIR" || strings.HasPrefix(key, "WI_") {
			scrub = append(scrub, key)
			delete(global, key)
		}
	}
	if _, ok := global["PATH"]; !ok {
		global["PATH"] = os.Getenv("PATH")
	}
	return global, scrub, warnings, nil
}

func envFromSlice(entries []string) map[string]string {
	env := map[string]string{}
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	return env
}

// agentRuntimeEnvironment returns the complete environment for an item runtime.
// Launchers must use it as a replacement, never as an overlay on their own
// process environment.
func (a *App) agentRuntimeEnvironment(ctx context.Context, m model.Manifest, mode agent.Mode) (map[string]string, []string, []string, error) {
	subject := plugin.Subject{ItemID: m.ID, RepositoryID: m.Repository.ID, Mode: string(mode)}
	if claim := m.Checkout.Claim; claim != nil {
		subject.CheckoutPath = claim.Path
		subject.ClaimID = claim.ID
		subject.Generation = claim.Generation
	}
	base, scrub, warnings, err := a.baseRuntimeEnvironment(ctx, mode, subject)
	if err != nil {
		return nil, scrub, warnings, err
	}
	overrides, removed, runtimeWarnings, err := a.Preparation.Environment(ctx, "runtime", plugin.EnvironmentRequest{Subject: subject, Values: base})
	warnings = append(warnings, runtimeWarnings...)
	scrub = append(scrub, removed...)
	if err != nil {
		return nil, scrub, warnings, err
	}
	base = overrides
	// A long-lived tmux server may predate installation of wi. Keep the
	// running executable discoverable without importing another item's PATH
	// or secrets from the caller. Apply after extensions so they cannot hide wi.
	if self := strings.TrimSpace(a.SelfPath); filepath.IsAbs(self) {
		directory := filepath.Dir(self)
		entries := filepath.SplitList(base["PATH"])
		if !slices.Contains(entries, directory) {
			if base["PATH"] == "" {
				base["PATH"] = directory
			} else {
				base["PATH"] = directory + string(os.PathListSeparator) + base["PATH"]
			}
		}
	}
	if mode == agent.ModeTUI && a.Tmux != nil {
		if current, inspectErr := a.Tmux.SessionEnvironment(ctx, m.TerminalSessionName()); inspectErr == nil {
			remove := make(map[string]bool, len(scrub)+len(current))
			for _, key := range scrub {
				remove[key] = true
			}
			for key := range current {
				if _, present := base[key]; !present {
					remove[key] = true
				}
			}
			scrub = scrub[:0]
			for key := range remove {
				scrub = append(scrub, key)
			}
			slices.Sort(scrub)
		}
	}
	return base, scrub, warnings, nil
}
