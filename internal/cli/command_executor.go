package cli

import (
	"context"
	"fmt"
	"github.com/regb/workitem/internal/app"
	"os"
)

func runExecutorAttach(ctx context.Context, args []string, cfg Config) error {
	if len(args) != 1 {
		return usageErr{fmt.Errorf("usage: wi internal-executor-attach <item-id>")}
	}
	return cfg.App.AttachExecutor(ctx, app.ResolveOptions{Selector: args[0], CWD: cfg.CWD, Env: cfg.Env}, os.Stdin, cfg.Stdout, cfg.Stderr)
}
