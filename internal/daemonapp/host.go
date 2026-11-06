package daemonapp

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/xdg"
)

// ExecHost is an internal daemon child entry point, not a public client command.
// The primary-agent service fences execution against its runtime owner record.
func ExecHost(ctx context.Context, args []string, client *coordinator.Client, paths xdg.Paths, cfg config.Config, host *plugin.Host, self string, env map[string]string) error {
	fs := flag.NewFlagSet("agent host", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	item := fs.String("item", "", "item")
	conversation := fs.String("conversation", "", "conversation")
	runtime := fs.String("runtime", "", "runtime")
	modeValue := fs.String("mode", "", "mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *item == "" || *conversation == "" || *runtime == "" || *modeValue == "" {
		return errors.New("internal agent host requires item, conversation, runtime and mode")
	}
	mode, err := agent.ParseMode(*modeValue)
	if err != nil {
		return err
	}
	application, err := Host(ctx, client, paths, cfg, host, self, env)
	if err != nil {
		return err
	}
	return application.AgentExec(ctx, *item, *conversation, *runtime, mode)
}
