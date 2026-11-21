// Command wi-usage is the bundled usage plugin for wi. Enable it with
//
//	[plugins.usage]
//	enabled = true
//
// and wi usage shows how much of each subscription's quota is left
// (Claude, OpenAI through Codex, OpenCode Go, Antigravity), asking each
// native client. It never sends a model prompt. It reads no credentials,
// except Antigravity's saved access token, read-only, for its quota endpoint.
package main

import (
	"context"
	"os"
	"os/signal"
	"strings"

	"github.com/regb/workitem/plugins/usage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env := map[string]string{}
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	os.Exit(usage.Run(ctx, os.Args[1:], usage.Options{Env: env, Stdout: os.Stdout, Stderr: os.Stderr}))
}
