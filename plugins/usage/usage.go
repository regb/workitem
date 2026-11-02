// Package usage is the bundled usage plugin: it shows how much of each
// subscription's quota is left (Claude, OpenAI through Codex, OpenCode Go),
// asking each native client, in text or JSON. wi usage runs it.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/regb/workitem/internal/usage"
	"github.com/regb/workitem/internal/usage/providers"
)

// Exit codes, as wi's: a usage error is 2, a local failure 1. A provider
// that fails is reported in its result, not by the exit code.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Options is what a run needs from its process; Providers replaces
// discovery in tests.
type Options struct {
	Env       map[string]string
	Stdout    io.Writer
	Stderr    io.Writer
	Providers []usage.Provider
}

var errUsage = errors.New("usage: wi usage [show] [--provider claude|codex|opencode-go] [--account ID] [--refresh] [--json]")

// Run shows remaining quota. Arguments are what follows wi usage: an
// optional show, its options, and wi's global --json anywhere.
func Run(ctx context.Context, args []string, opts Options) int {
	jsonOut := false
	rest := []string{}
	for _, arg := range args {
		if arg == "--json" {
			jsonOut = true
			continue
		}
		rest = append(rest, arg)
	}
	if len(rest) > 0 && rest[0] == "show" {
		rest = rest[1:]
	}
	err := run(ctx, rest, opts, jsonOut)
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, new(usageError)):
		fmt.Fprintln(opts.Stderr, err)
		return ExitUsage
	default:
		fmt.Fprintln(opts.Stderr, err)
		return ExitError
	}
}

type usageError struct{ error }

func run(ctx context.Context, args []string, opts Options, jsonOut bool) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	provider := fs.String("provider", "", "claude/anthropic, codex/openai, or opencode-go/opencode")
	account := fs.String("account", "", "native account/configuration binding ID")
	refresh := fs.Bool("refresh", false, "refresh remaining quota, subject to cooldown")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() != 0 {
		return usageError{errUsage}
	}
	*provider = providers.CanonicalProvider(*provider)
	if *provider != "" && *provider != "claude" && *provider != "codex" && *provider != "opencode-go" {
		return usageError{fmt.Errorf("--provider must be claude/anthropic, codex/openai, or opencode-go/opencode")}
	}
	selected := opts.Providers
	if selected == nil {
		var err error
		selected, err = providers.Discover(opts.Env, *provider)
		if err != nil {
			return err
		}
	}
	root, err := providers.CacheRoot(opts.Env)
	if err != nil {
		return err
	}
	cache := usage.Cache{Root: root}
	quotas := []usage.Quota{}
	for _, p := range selected {
		if *provider != "" && p.Account().Provider != *provider {
			continue
		}
		if *account != "" {
			if resolver, ok := p.(usage.AccountResolver); ok {
				p = resolver.ResolveAccount(ctx)
			}
			if p.Account().ID != *account {
				continue
			}
		}
		q, err := cache.Get(ctx, p, *refresh)
		if err != nil {
			return err
		}
		quotas = append(quotas, q)
	}
	if *account != "" && len(quotas) == 0 {
		return usageError{fmt.Errorf("no selected native account/configuration matches --account")}
	}
	if jsonOut {
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(usage.NewSnapshot(quotas))
	}
	return writeUsageText(opts.Stdout, quotas, time.Now().UTC())
}
