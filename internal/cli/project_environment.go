package cli

import (
	"context"
	"maps"

	"github.com/regb/workitem/internal/plugin"
)

var cliProjectVariables = []string{"WI_LIST_LABELS", "WI_ITEM_DEFAULT_LABELS"}

func commandUsesCLIProjectEnvironment(args []string) bool {
	filtered, _ := extractGlobals(args)
	if len(filtered) == 0 {
		return false
	}
	switch filtered[0] {
	case "new", "list", "start", "switch", "next", "attention":
		return true
	}
	return false
}
func loadCLIProjectEnvironment(ctx context.Context, pipeline plugin.Pipeline, cwd, boundary, repositoryID string, source map[string]string) (map[string]string, []string, error) {
	values, _, warnings, err := pipeline.Environment(ctx, "command", plugin.EnvironmentRequest{Subject: plugin.Subject{CheckoutPath: cwd, BoundaryPath: boundary, RepositoryID: repositoryID}, Values: source, Source: source, AllowedKeys: cliProjectVariables})
	return values, warnings, err
}
func cloneEnvironment(env map[string]string) map[string]string { return maps.Clone(env) }
