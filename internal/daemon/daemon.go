// Package daemon composes and serves a wi daemon: its coordinator, the
// application use cases, the public API and plugin jobs. `wi daemon serve`
// starts the daemon through it, and so do the API test fixtures, so both run
// the same composition.
package daemon

import (
	"context"
	"path/filepath"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/daemonapi"
	"github.com/regb/workitem/internal/daemonapp"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/xdg"
)

// Options are what a daemon is composed from.
type Options struct {
	// Paths are the daemon's data, state and runtime directories.
	Paths xdg.Paths
	// Socket is the public API socket; the internal one is derived from it.
	Socket string
	// Config and Host are the loaded configuration and its plugin host.
	Config config.Config
	Host   *plugin.Host
	// Self is the wi executable: it hosts agent runtimes and bundled drivers
	// and plugins.
	Self string
	// Env is the daemon's environment, which drivers and agent runtimes
	// start from.
	Env map[string]string
}

// Daemon is a composed daemon, ready to serve.
type Daemon struct {
	Core    *coordinator.Server
	App     *app.App
	API     *daemonapi.Server
	options Options
}

// New composes a daemon. The coordinator listens on its socket once New
// returns; Serve answers requests. ctx bounds the daemon's background work.
func New(ctx context.Context, o Options) (*Daemon, error) {
	core, err := coordinator.NewServerAt(coordinator.ServerConfig{Paths: o.Paths, Socket: o.Socket, Decoders: daemonapp.ConversationDecoders(o.Config, o.Self, o.Env)})
	if err != nil {
		return nil, err
	}
	core.ConfigureRepositorySettings(o.Host.Registry.Options())
	core.SetManaged(o.Config.Daemon.Managed)
	application, err := daemonapp.New(ctx, core, o.Paths, o.Config, o.Host, o.Self, o.Env)
	if err != nil {
		core.Close()
		return nil, err
	}
	application.DaemonSocketPath = coordinator.InternalSocketPath(o.Socket)
	api, err := daemonapi.New(ctx, core, application, o.Config, o.Host, o.Paths.DataRoot)
	if err != nil {
		core.Close()
		return nil, err
	}
	core.SetBeforeClose(api.Close)
	return &Daemon{Core: core, App: application, API: api, options: o}, nil
}

// Serve answers API requests and runs plugin jobs until ctx ends or the
// coordinator closes.
func (d *Daemon) Serve(ctx context.Context) error {
	pluginCtx, cancelPlugins := context.WithCancel(ctx)
	defer cancelPlugins()
	pluginsDone := make(chan struct{})
	go func() {
		defer close(pluginsDone)
		plugin.Runner{Host: d.options.Host, WiPath: d.options.Self, LogDir: filepath.Join(d.options.Paths.DataStateRoot, "plugins")}.Run(pluginCtx)
	}()
	err := d.Core.ServeAPI(ctx, d.API.Handler())
	cancelPlugins()
	<-pluginsDone
	return err
}

// Close stops the API, then the coordinator.
func (d *Daemon) Close() error { return d.Core.Close() }
