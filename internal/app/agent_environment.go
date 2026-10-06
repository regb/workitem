package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	direnv "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/model"
)

func exactRuntimeEnvironment(environment map[string]string) []string {
	return agentcore.MergeEnvironment(nil, environment)
}

// direnvBaseEnvironment returns the clean environment that a tmux session
// should start from: the tmux server's global environment with any variables
// that another worktree's .envrc exported removed. A long-lived tmux server
// captures whatever shell started it, which can leak project secrets into every
// session; decoding DIRENV_DIFF (from both the server and the invoking pane)
// identifies exactly those variables. Without tmux (headless), the process
// environment is the only sensible base.
func (a *App) direnvBaseEnvironment(ctx context.Context) (map[string]string, []string) {
	var global map[string]string
	if a.Tmux != nil {
		if candidate, err := a.Tmux.GlobalEnvironment(ctx); err == nil && len(candidate) > 0 {
			global = candidate
		}
	}
	if len(global) == 0 {
		global = envFromSlice(os.Environ())
	}
	managed := direnv.ManagedVariables(global["DIRENV_DIFF"])
	for key := range direnv.ManagedVariables(os.Getenv("DIRENV_DIFF")) {
		managed[key] = true
	}
	scrub := make([]string, 0, len(managed)+8)
	for key := range global {
		if key == "TMUX" || key == "TMUX_PANE" || key == "DIRENV_DIFF" || strings.HasPrefix(key, "DIRENV_") || strings.HasPrefix(key, "WI_") {
			scrub = append(scrub, key)
			delete(global, key)
			continue
		}
		if key == "PATH" || key == "HOME" || isShellRuntimeVariable(key) {
			continue
		}
		if managed[key] {
			scrub = append(scrub, key)
			delete(global, key)
		}
	}
	if _, ok := global["PATH"]; !ok {
		global["PATH"] = os.Getenv("PATH")
	}
	return global, scrub
}

func isShellRuntimeVariable(key string) bool {
	switch key {
	case "PWD", "OLDPWD", "SHLVL", "_", "PROMPT_COMMAND", "TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "VTE_VERSION", "SHELL":
		return true
	}
	return false
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
func (a *App) agentRuntimeEnvironment(ctx context.Context, m model.Manifest) (map[string]string, []string, []string, error) {
	base, scrub := a.direnvBaseEnvironment(ctx)
	environmentManifest := m
	if claim := m.Checkout.Claim; claim != nil && claim.Executor != "" && claim.Executor != model.ExecutorLocal {
		environmentManifest.Checkout.Claim = nil
	}
	overrides, warnings, err := agentcore.RuntimeEnvironment(ctx, a.Direnv, environmentManifest, a.direnvAutoTrusts(m.Repository), a.ApproveDirenv, base)
	if err != nil {
		return nil, scrub, warnings, err
	}
	for key, value := range overrides {
		base[key] = value
	}
	if a.Tmux != nil {
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

func (a *App) direnvAutoTrusts(repo model.Repository) bool {
	candidates := []string{repo.OperationalRoot(), repo.RootAtCreation}
	if common := strings.TrimSpace(repo.GitCommonDir); filepath.Base(common) == ".git" {
		candidates = append(candidates, filepath.Dir(common))
	}
	for _, configured := range a.DirenvConfig.AutoTrustRepositories {
		trusted, err := filepath.Abs(strings.TrimSpace(configured))
		if err != nil {
			continue
		}
		for _, candidate := range candidates {
			resolved, err := filepath.Abs(strings.TrimSpace(candidate))
			if err == nil && resolved == trusted {
				return true
			}
		}
	}
	return false
}
