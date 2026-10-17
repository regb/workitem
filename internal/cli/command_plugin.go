package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/plugin"
)

func runPlugin(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{fmt.Errorf("usage: wi plugin <list|run|skill>")}
	}
	host := cfg.Plugins
	if host == nil && cfg.App != nil {
		host = cfg.App.Plugins
	}
	if host == nil {
		return fmt.Errorf("plugin catalog is unavailable")
	}
	specs, err := host.Specs(ctx, "")
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		fs := newFlagSet("plugin list", cfg.Stderr)
		sensitive := fs.Bool("show-sensitive", false, "reveal private plugin values")
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr{err}
		}
		if fs.NArg() != 0 {
			return usageErr{fmt.Errorf("usage: wi plugin list [--show-sensitive]")}
		}
		type listed struct {
			plugin.Spec
			Log   string `json:"log"`
			Hooks bool   `json:"preparation_hooks"`
		}
		result := []listed{}
		for _, spec := range specs {
			if !*sensitive {
				spec.Command = []string{"[redacted]"}
				raw, _ := json.Marshal(spec.Settings)
				var settings map[string]any
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				if err := decoder.Decode(&settings); err != nil {
					return err
				}
				tree := map[string]any{spec.Name: settings}
				host.Registry.Options().Redact(tree)
				spec.Settings = settings
			}
			native := false
			for _, d := range host.Registry.Definitions() {
				if d.Name == spec.Name {
					native = d.Native != nil
				}
			}
			result = append(result, listed{Spec: spec, Log: plugin.LogPath(filepath.Join(cfg.StateRoot, "plugins"), spec.Name), Hooks: native})
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		for _, entry := range result {
			state := "disabled"
			if entry.Enabled {
				state = "enabled"
			}
			kind := "executable"
			if entry.Hooks {
				kind = "native preparation"
			}
			fmt.Fprintf(cfg.Stdout, "%s  %s  %s  %s\n  log: %s\n", entry.Name, state, kind, strings.Join(entry.Command, " "), entry.Log)
		}
		return nil
	case "skill":
		if len(args) != 2 {
			return usageErr{fmt.Errorf("usage: wi plugin skill NAME")}
		}
		for _, spec := range specs {
			if spec.Name == args[1] {
				text, err := plugin.Skill(ctx, spec, host.SelfPath)
				if err != nil {
					return err
				}
				if jsonOut {
					return writeJSON(cfg.Stdout, map[string]any{"name": spec.Name, "enabled": spec.Enabled, "markdown": text})
				}
				fmt.Fprintln(cfg.Stdout, text)
				return nil
			}
		}
	case "run":
		if cfg.ControlAccess == coordinator.AccessAgent || cfg.Env["WI_AGENT_DAEMON_SOCKET"] != "" {
			return fmt.Errorf("agent endpoint does not allow manual plugin runs")
		}
		if len(args) != 2 {
			return usageErr{fmt.Errorf("usage: wi plugin run NAME")}
		}
		found := false
		for _, definition := range host.Registry.Definitions() {
			if definition.Name != args[1] {
				continue
			}
			found = true
			if len(definition.Jobs) == 0 {
				return fmt.Errorf("plugin %s has no jobs", definition.Name)
			}
			for _, job := range definition.Jobs {
				ids := []string{""}
				if job.Repository {
					if host.ListRepositories == nil {
						return fmt.Errorf("repository source is unavailable")
					}
					ids, err = host.ListRepositories(ctx)
					if err != nil {
						return err
					}
				}
				for _, id := range ids {
					fresh := host.Fresh()
					fresh.Registrations[definition.Name] = plugin.Registration{Enabled: true, Command: host.Registrations[definition.Name].Command}
					jobs, err := fresh.JobSpecs(ctx, id)
					if err != nil {
						return err
					}
					for _, spec := range jobs {
						if spec.Name == definition.Name && spec.JobName == job.Name {
							if err := plugin.RunOnce(ctx, spec, host.SelfPath, cfg.Stdout, cfg.Stderr); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		if found {
			return nil
		}
	default:
		return usageErr{fmt.Errorf("unknown plugin action %q", args[0])}
	}
	return fmt.Errorf("plugin %q is not registered", args[len(args)-1])
}

func runPluginCommand(ctx context.Context, name string, args []string, cfg Config, jsonOut bool) (int, bool) {
	host := cfg.Plugins
	if host == nil && cfg.App != nil {
		host = cfg.App.Plugins
	}
	if host == nil {
		return 0, false
	}
	specs, err := host.Specs(ctx, "")
	if err != nil {
		fmt.Fprintln(cfg.Stderr, err)
		return ExitError, true
	}
	for _, spec := range specs {
		if spec.Name != name || !spec.Enabled {
			continue
		}
		if len(args) == 0 {
			fmt.Fprintln(cfg.Stderr, "plugin subcommand is required")
			return ExitUsage, true
		}
		declared := false
		for _, command := range spec.Commands {
			declared = declared || args[0] == command
		}
		if !declared {
			fmt.Fprintf(cfg.Stderr, "unregistered plugin command %s %s\n", name, args[0])
			return ExitUsage, true
		}
		selector := ""
		for i, arg := range args {
			if arg == "--item" && i+1 < len(args) {
				selector = args[i+1]
			}
			if strings.HasPrefix(arg, "--item=") {
				selector = strings.TrimPrefix(arg, "--item=")
			}
		}
		if cfg.App != nil {
			if m, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}); err == nil {
				spec.RepositoryID = m.Repository.ID
				spec.Settings, err = host.Settings(ctx, m.Repository.ID, name)
				if err != nil {
					fmt.Fprintln(cfg.Stderr, err)
					return ExitError, true
				}
			} else if selector != "" {
				fmt.Fprintln(cfg.Stderr, err)
				return ExitError, true
			}
		}
		if jsonOut {
			args = append([]string{"--json"}, args...)
		}
		env := []string{}
		for key, value := range cfg.Env {
			if key != "WI_EXECUTABLE" && key != "WI_PLUGIN" && key != "WI_PLUGIN_CONFIG" && key != "WI_PLUGIN_REPOSITORY_ID" {
				env = append(env, key+"="+value)
			}
		}
		env = append(env, plugin.Variables(spec, host.SelfPath)...)
		if err := plugin.RunCommand(ctx, spec, args, env, cfg.CWD, cfg.Stdin, cfg.Stdout, cfg.Stderr); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return exit.ExitCode(), true
			}
			fmt.Fprintln(cfg.Stderr, err)
			return ExitError, true
		}
		return ExitOK, true
	}
	return 0, false
}
