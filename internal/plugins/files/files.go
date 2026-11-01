// Package files supplies the bundled plugin that copies a repository's files
// (internal/repofiles) into each claimed checkout before setup, and keeps Git
// ignoring them through the owned store's info/exclude.
package files

import (
	"context"
	"slices"

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

func setup(ctx context.Context, request plugin.SetupRequest, settings map[string]any) ([]string, error) {
	// Ignore the files before they land, and stop ignoring removed ones after.
	if err := repofiles.UpdateExclude(ctx, request.RepositoryRoot, request.FilesDir); err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, p := range stringsSetting(settings["skip"]) {
		skip[p] = true
	}
	warnings, err := repofiles.CopyInto(ctx, request.Subject.CheckoutPath, request.FilesDir, request.Subject.ClaimID, skip, request.Log)
	if err != nil {
		return warnings, err
	}
	return warnings, repofiles.UpdateExclude(ctx, request.RepositoryRoot, request.FilesDir)
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
