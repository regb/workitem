package cli

import (
	"context"
	"fmt"

	"github.com/regb/workitem/internal/app"
)

func repositorySelector(ctx context.Context, cfg Config, verb, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if cfg.App == nil {
		return "", usageErr{fmt.Errorf("wi repo %s requires NAME outside a current work item", verb)}
	}
	current, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return "", usageErr{fmt.Errorf("select a repository with wi repo %s NAME: %w", verb, err)}
	}
	if current.Repository.ID == "" {
		return "", usageErr{fmt.Errorf("current work item has no registered repository; pass wi repo %s NAME", verb)}
	}
	return current.Repository.ID, nil
}
