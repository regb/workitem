package primaryagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/model"
)

type Direnv interface {
	Status(context.Context, string) (model.DirenvStatus, error)
	Environment(context.Context, string, map[string]string) (map[string]string, error)
	Allow(context.Context, string) error
}

type DirenvApprover func(context.Context, model.Manifest, string) (bool, error)

func MergeEnvironment(base []string, extra map[string]string) []string {
	values, order := map[string]string{}, []string{}
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	for key, value := range extra {
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	out := make([]string, 0, len(values))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}

func RuntimeEnvironment(ctx context.Context, direnv Direnv, m model.Manifest, autoTrust bool, approve DirenvApprover, base map[string]string) (map[string]string, []string, error) {
	env, warnings := map[string]string{}, []string{}
	if direnv == nil || m.Checkout.Path == nil || strings.TrimSpace(*m.Checkout.Path) == "" {
		return env, warnings, nil
	}
	status, err := direnv.Status(ctx, *m.Checkout.Path)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file or directory") {
			return env, warnings, nil
		}
		return env, append(warnings, "could not inspect worktree direnv environment: "+err.Error()), nil
	}
	if !status.Found {
		return env, warnings, nil
	}
	if !status.Allowed {
		approved := autoTrust
		if !approved && approve != nil {
			approved, err = approve(ctx, m, status.RCPath)
			if err != nil {
				return env, warnings, fmt.Errorf("request direnv approval: %w", err)
			}
		}
		if !approved {
			return env, append(warnings, fmt.Sprintf("worktree .envrc is not trusted; agent runtime will start without it (run `direnv allow %s` to trust it)", status.RCPath)), nil
		}
		if err := direnv.Allow(ctx, status.RCPath); err != nil {
			return env, warnings, fmt.Errorf("allow worktree .envrc: %w", err)
		}
		if autoTrust {
			warnings = append(warnings, "auto-trusted worktree .envrc because its repository is in user config direnv.auto_trust_repositories")
		} else {
			warnings = append(warnings, "trusted worktree .envrc after operator approval")
		}
	}
	exported, err := direnv.Environment(ctx, *m.Checkout.Path, base)
	if err != nil {
		return env, warnings, fmt.Errorf("load trusted worktree .envrc for agent runtime: %w", err)
	}
	for key, value := range exported {
		if key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "DIRENV_") {
			continue
		}
		if (strings.HasPrefix(key, "WI_") && key != "WI_LIST_LABELS" && key != "WI_ITEM_DEFAULT_LABELS") || key == "PI_CODING_AGENT_SESSION_DIR" {
			warnings = append(warnings, fmt.Sprintf("ignored reserved direnv variable %s", key))
			continue
		}
		env[key] = value
	}
	return env, warnings, nil
}
