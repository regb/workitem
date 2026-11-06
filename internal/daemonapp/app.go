// Package daemonapp composes application use cases inside the daemon.
package daemonapp

import (
	"context"
	"fmt"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/tmux"
	"github.com/regb/workitem/internal/xdg"
	"os"
)

func New(ctx context.Context, server *coordinator.Server, paths xdg.Paths, cfg config.Config, host *plugin.Host, self string, env map[string]string) (*app.App, error) {
	return withClient(ctx, server.LocalClient(), paths, cfg, host, self, env, true)
}

// Host composes a daemon-owned runtime child against its private host endpoint.
func Host(ctx context.Context, client *coordinator.Client, paths xdg.Paths, cfg config.Config, host *plugin.Host, self string, env map[string]string) (*app.App, error) {
	return withClient(ctx, client, paths, cfg, host, self, env, false)
}

func withClient(ctx context.Context, local *coordinator.Client, paths xdg.Paths, cfg config.Config, host *plugin.Host, self string, env map[string]string, seed bool) (*app.App, error) {
	native := store.New(paths.DataRoot)
	a := app.New(native, git.New("git"))
	a.DeepWorkConfig, a.ItemConfig, a.ListConfig, a.RepoConfig = cfg.DeepWork, cfg.Item, cfg.List, cfg.Repo
	a.AgentConfig, a.AgentStatusConfig, a.AttentionConfig, a.DriversConfig = cfg.Agent, cfg.AgentStatus, cfg.Attention, cfg.Drivers
	a.Plugins, a.Tmux, a.SelfPath = host, tmux.New("tmux"), self
	a.AgentRuntimeStateRoot, a.AgentRuntimeSocketRoot = paths.DataStateRoot, paths.DataRuntimeRoot
	for _, driver := range []string{"pi", "claude"} {
		a.BundledDriverCommands[driver] = app.BundledDriverCommand{Path: self, Args: []string{"internal-driver", driver}}
	}
	clients := configuredDriverClients(cfg, self, env)
	for name, client := range clients {
		a.ConversationBackends[name] = client
	}
	Bind(a, native, local)
	a.LoadAgentProfiles = local.AgentProfiles
	discoverers := map[string]configDiscoverer{}
	for name, client := range clients {
		discoverers[name] = client
	}
	if err := func() error {
		if !seed {
			return nil
		}
		return seedAgentProfiles(ctx, local, discoverers, os.Stderr)
	}(); err != nil {
		return nil, fmt.Errorf("agent profiles: %w", err)
	}
	a.RepositoryEnvironment = func(ctx context.Context, id string) ([]model.EnvVar, error) {
		result, err := local.RepositoryEnvironment(ctx, id)
		return result.Environment.Vars, err
	}
	host.ReadRepository = func(ctx context.Context, id string) (model.RepositorySettings, error) {
		result, err := local.RepositorySettings(ctx, id)
		return result.Settings, err
	}
	host.ListRepositories = func(ctx context.Context) ([]string, error) {
		repos, err := local.ListRepositories(ctx)
		ids := []string{}
		for _, repo := range repos {
			ids = append(ids, repo.ID)
		}
		return ids, err
	}
	a.Preparation = host.Preparation(func(ctx context.Context, request plugin.Approval) (bool, error) {
		if approve := plugin.ContextApprover(ctx); approve != nil {
			return approve(ctx, request)
		}
		return false, &plugin.ApprovalRequired{Approval: request}
	})
	return a, nil
}
