package daemonapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/regb/workitem/internal/plugins/bundled"
	"os"
	"strconv"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/version"
)

func (s *Server) GetInfo(ctx context.Context, req *connect.Request[v1.GetInfoRequest]) (*connect.Response[v1.GetInfoResponse], error) {
	access, kind := v1.CallerAccess_CALLER_ACCESS_OPERATOR, v1.CallerKind_CALLER_KIND_LOCAL_OPERATOR
	switch apitransport.Caller(ctx) {
	case apitransport.Agent:
		access, kind = v1.CallerAccess_CALLER_ACCESS_AGENT, v1.CallerKind_CALLER_KIND_AGENT
	case apitransport.WebAdapter:
		kind = v1.CallerKind_CALLER_KIND_WEB_ADAPTER
	}
	info := &v1.GetInfoResponse{Caller: &v1.Caller{Access: access, Kind: kind}, Version: version.Current().Version, ApiVersion: "1.0", DataRoot: s.Root, DefaultAgentProfile: s.App.DefaultProfile(), StartedAt: stamp(s.StartedAt), Managed: s.Config.Daemon.Managed}
	if apitransport.Caller(ctx) == apitransport.Agent {
		info.DataRoot = ""
	}
	out := connect.NewResponse(info)
	out.Header().Set("Wi-Daemon-Pid", strconv.Itoa(os.Getpid()))
	out.Header().Set("Wi-Build-Identity", version.BuildIdentity())
	out.Header().Set("Wi-Database-Schema", strconv.Itoa(coordinator.SchemaVersion))
	return out, nil
}
func (s *Server) GetHealth(ctx context.Context, req *connect.Request[v1.GetHealthRequest]) (*connect.Response[v1.GetHealthResponse], error) {
	_, err := s.Core.Database().Status()
	out := &v1.GetHealthResponse{Healthy: err == nil, LastObservationAt: stamp(time.Now())}
	if err != nil {
		out.Problems = []string{err.Error()}
	}
	return response(out, nil)
}
func (s *Server) RunDiagnostics(ctx context.Context, req *connect.Request[v1.RunDiagnosticsRequest]) (*connect.Response[v1.RunDiagnosticsResponse], error) {
	status, err := s.Core.Database().Status()
	out := &v1.RunDiagnosticsResponse{Diagnostics: []*v1.Diagnostic{{Check: "database", Ok: err == nil, Detail: fmt.Sprintf("schema %d, sequence %d", status.SchemaVersion, status.GlobalSequence)}}}
	if err != nil {
		out.Diagnostics[0].Detail = err.Error()
	}
	barrier, e := s.Local.ActivityBarrier(ctx)
	out.Diagnostics = append(out.Diagnostics, &v1.Diagnostic{Check: "observations", Ok: e == nil, Detail: fmt.Sprint(barrier.Projection.Warnings)})
	if e != nil {
		out.Diagnostics[1].Detail = e.Error()
	}
	return response(out, nil)
}
func (s *Server) GetConfig(ctx context.Context, req *connect.Request[v1.GetConfigRequest]) (*connect.Response[v1.GetConfigResponse], error) {
	raw, err := json.Marshal(s.Config)
	if err != nil {
		return nil, err
	}
	var tree map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&tree); err != nil {
		return nil, err
	}
	if item, ok := tree["item"].(map[string]any); ok {
		defaults, _ := item["defaults"].(map[string]any)
		if defaults == nil {
			defaults = map[string]any{}
		}
		defaults["labels"] = append([]string{}, s.Config.Item.Defaults.Labels...)
		item["defaults"] = defaults
	}
	if drivers, ok := tree["drivers"].(map[string]any); ok {
		for name, driver := range s.Config.Drivers {
			entry, _ := drivers[name].(map[string]any)
			if entry != nil {
				entry["command"] = driver.Command
				if _, found := entry["defaults"]; !found {
					entry["defaults"] = map[string]any{}
				}
			}
		}
	}
	optionValues := s.Config.PluginSettings
	if s.Plugins != nil {
		optionValues = s.Plugins.Registry.Options().Resolve(s.Config.PluginSettings, nil)
	}
	optionBytes, err := json.Marshal(optionValues)
	if err != nil {
		return nil, err
	}
	options := map[string]any{}
	decoder = json.NewDecoder(bytes.NewReader(optionBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&options); err != nil {
		return nil, err
	}
	if !req.Msg.ShowSensitive {
		if drivers, ok := tree["drivers"].(map[string]any); ok {
			for _, entry := range drivers {
				if d, ok := entry.(map[string]any); ok {
					if _, found := d["defaults"]; found {
						d["defaults"] = "[redacted]"
					}
					if _, found := d["command"]; found {
						d["command"] = "[redacted]"
					}
				}
			}
		}
		if s.Plugins != nil {
			s.Plugins.Registry.Options().Redact(options)
		}
	}
	plugins, _ := tree["plugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}
	for name, values := range options {
		entry, _ := plugins[name].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}
		if values, ok := values.(map[string]any); ok {
			for key, value := range values {
				entry[key] = value
			}
		}
		plugins[name] = entry
	}
	if !req.Msg.ShowSensitive {
		for _, entry := range plugins {
			if p, ok := entry.(map[string]any); ok {
				if _, found := p["command"]; found {
					p["command"] = "[redacted]"
				}
			}
		}
	}
	tree["plugins"] = plugins
	origins := s.Config.PluginOrigins()
	if origins == nil {
		origins = map[string]string{}
	}
	if s.Plugins != nil {
		for _, definition := range s.Plugins.Registry.Options().Definitions() {
			for _, option := range definition.Options {
				key := "plugins." + definition.Name + "." + option.Name
				if _, exists := origins[key]; !exists {
					origins[key] = "default"
				}
			}
		}
	}
	tree["origins"] = origins
	raw, err = json.Marshal(tree)
	return response(&v1.GetConfigResponse{ConfigJson: string(raw)}, err)
}
func (s *Server) ListPlugins(ctx context.Context, req *connect.Request[v1.ListPluginsRequest]) (*connect.Response[v1.ListPluginsResponse], error) {
	out := &v1.ListPluginsResponse{Plugins: []*v1.Plugin{}}
	if s.Plugins == nil {
		return response(out, nil)
	}
	specs, err := s.Plugins.Specs(ctx, "")
	if err != nil {
		return nil, rpcError(err)
	}
	for _, spec := range specs {
		p := &v1.Plugin{Name: spec.Name, Enabled: spec.Enabled, Commands: spec.Commands}
		for _, def := range bundled.Registry().Definitions() {
			if def.Name == spec.Name {
				p.Bundled = true
			}
		}
		for _, def := range s.Plugins.Registry.Definitions() {
			if def.Name != spec.Name || def.Native == nil {
				continue
			}
			hooks := def.Native(nil)
			if hooks.SetupCheckout != nil {
				p.Hooks = append(p.Hooks, "setup_checkout")
			}
			if hooks.PrepareCheckout != nil {
				p.Hooks = append(p.Hooks, "prepare_checkout")
			}
			if hooks.RuntimeEnvironment != nil || hooks.CommandEnvironment != nil {
				p.Hooks = append(p.Hooks, "agent_environment")
			}
			if hooks.Sanitize != nil {
				p.Hooks = append(p.Hooks, "sanitize_environment")
			}
		}
		for _, def := range s.Plugins.Registry.Options().Definitions() {
			if def.Name != spec.Name {
				continue
			}
			for _, opt := range def.Options {
				raw, _ := json.Marshal(opt.Default)
				if opt.Secret {
					raw = nil
				}
				p.Options = append(p.Options, &v1.PluginOption{Name: opt.Name, Type: opt.Type, Description: opt.Description, Sensitive: opt.Secret, RepositoryScope: opt.Repository, DefaultJson: string(raw)})
			}
		}
		out.Plugins = append(out.Plugins, p)
	}
	return response(out, nil)
}
func (s *Server) Shutdown(ctx context.Context, req *connect.Request[v1.ShutdownRequest]) (*connect.Response[v1.ShutdownResponse], error) {
	if s.Config.Daemon.Managed {
		return nil, precondition(coordinator.ManagedDaemonMessage)
	}
	job := s.startJob("shutdown", "", "", func(ctx context.Context, j *jobRun) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		result, err := s.App.ShutdownAll(ctx, nil, req.Msg.Force)
		if err != nil {
			return err
		}
		if !result.Complete() {
			return precondition("some agents or terminals could not be stopped")
		}
		go func() { time.Sleep(300 * time.Millisecond); s.Core.Close() }()
		return nil
	})
	return response(&v1.ShutdownResponse{Job: job}, nil)
}
