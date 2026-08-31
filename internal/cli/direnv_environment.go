package cli

import (
	"context"
	"fmt"
	"strings"

	direnvpkg "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/model"
)

var cliDirenvVariables = []string{"WI_LIST_LABELS", "WI_ITEM_DEFAULT_LABELS"}

type cliDirenv interface {
	Status(context.Context, string) (model.DirenvStatus, error)
	Environment(context.Context, string, map[string]string) (map[string]string, error)
}

func commandUsesCLIProjectEnvironment(args []string) bool {
	filtered, _ := extractGlobals(args)
	if len(filtered) == 0 {
		return false
	}
	switch filtered[0] {
	case "new", "list", "start", "switch", "next", "attention":
		return true
	default:
		return false
	}
}

// loadCLIProjectEnvironment imports only wi's project-scoped CLI settings from
// an allowed .envrc. Explicit caller values win. Values inherited from another
// direnv directory are identified through DIRENV_DIFF and replaced or removed.
func loadCLIProjectEnvironment(ctx context.Context, client cliDirenv, cwd string, source map[string]string) (map[string]string, error) {
	effective := cloneEnvironment(source)
	managed := direnvpkg.ManagedVariables(source["DIRENV_DIFF"])
	for _, key := range cliDirenvVariables {
		if managed[key] {
			delete(effective, key)
		}
	}
	if client == nil || strings.TrimSpace(cwd) == "" {
		return effective, nil
	}
	status, err := client.Status(ctx, cwd)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file or directory") {
			return effective, nil
		}
		return nil, fmt.Errorf("inspect current direnv environment: %w", err)
	}
	if !status.Found || !status.Allowed {
		return effective, nil
	}
	base := cloneEnvironment(source)
	for key := range managed {
		delete(base, key)
	}
	for key := range base {
		if key == "DIRENV_DIFF" || strings.HasPrefix(key, "DIRENV_") {
			delete(base, key)
		}
	}
	exported, err := client.Environment(ctx, cwd, base)
	if err != nil {
		return nil, fmt.Errorf("load current direnv environment: %w", err)
	}
	for _, key := range cliDirenvVariables {
		if _, explicit := source[key]; explicit && !managed[key] {
			continue
		}
		if value, ok := exported[key]; ok {
			effective[key] = value
		} else {
			delete(effective, key)
		}
	}
	return effective, nil
}

func cloneEnvironment(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
