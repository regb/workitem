package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/xdg"
	"google.golang.org/protobuf/types/known/structpb"
)

func rpcInfo(ctx context.Context, command string, args []string, cfg Config, jsonOut bool) error {
	if len(args) > 1 {
		return usageErr{errors.New("usage: wi info [key]")}
	}
	value, err := cfg.API.System.GetInfo(ctx, rpcRequest(cfg, &v1.GetInfoRequest{}))
	if err != nil {
		return err
	}
	if command == "version" {
		if len(args) != 0 {
			return usageErr{errors.New("version accepts no keys")}
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintln(cfg.Stdout, "wi", value.Msg.Version)
		return nil
	}
	paths := cfg.Paths
	if paths.DataRoot == "" {
		paths, err = xdg.Resolve(xdg.Map(cfg.Env), cfg.Env["HOME"])
		if err != nil {
			return err
		}
	}
	operator, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return err
	}
	agent, err := coordinator.AgentSocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return err
	}
	if cfg.Env["WI_AGENT_DAEMON_SOCKET"] != "" {
		agent = cfg.Env["WI_AGENT_DAEMON_SOCKET"]
	}
	entries := []struct{ key, value string }{{"data-root", paths.DataRoot}, {"database", filepath.Join(paths.DataRoot, "wi.db")}, {"config-file", paths.ConfigFile}, {"cache-root", paths.CacheRoot}, {"state-root", paths.DataStateRoot}, {"runtime-root", paths.DataRuntimeRoot}, {"operator-socket", operator}, {"agent-socket", agent}}
	if len(args) == 1 {
		for _, entry := range entries {
			if entry.key == args[0] {
				if jsonOut {
					object, _ := structpb.NewStruct(map[string]any{strings.ReplaceAll(entry.key, "-", "_"): entry.value})
					return rpcEmit(cfg, true, object)
				}
				fmt.Fprintln(cfg.Stdout, entry.value)
				return nil
			}
		}
		return usageErr{fmt.Errorf("unknown info key %q", args[0])}
	}
	if jsonOut {
		return rpcEmit(cfg, true, value.Msg)
	}
	for _, entry := range entries {
		fmt.Fprintf(cfg.Stdout, "%s: %s\n", entry.key, entry.value)
	}
	fmt.Fprintf(cfg.Stdout, "version: %s\nAPI: %s\n", value.Msg.Version, value.Msg.ApiVersion)
	return nil
}
func rpcConfig(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("config", cfg.Stderr)
	origins := fs.Bool("origins", false, "show value origins")
	sensitive := fs.Bool("show-sensitive", false, "include secrets")
	keys := fs.Bool("keys", false, "keys only")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	pos := fs.Args()
	command := "show"
	key := ""
	if len(pos) > 0 {
		command = pos[0]
	}
	switch command {
	case "show", "drivers", "plugins":
		if len(pos) > 1 {
			return usageErr{errors.New("config accepts one command")}
		}
		if command == "drivers" {
			key = "drivers"
		}
	case "get":
		if len(pos) != 2 || strings.TrimSpace(pos[1]) == "" {
			return usageErr{errors.New("usage: wi config get KEY [--origins]")}
		}
		key = pos[1]
	case "agents":
		return usageErr{errors.New("agent profiles live in the daemon; use wi agent profile list")}
	default:
		// Retain direct dotted queries accepted by the first API client.
		if len(pos) == 1 && strings.Contains(command, ".") {
			command = "get"
			key = pos[0]
		} else {
			return usageErr{fmt.Errorf("unknown config command %q", command)}
		}
	}
	if command == "plugins" {
		if *origins {
			return usageErr{errors.New("--origins applies to value queries, not schema summaries")}
		}
		value, err := cfg.API.System.ListPlugins(ctx, rpcRequest(cfg, &v1.ListPluginsRequest{}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	response, err := cfg.API.System.GetConfig(ctx, rpcRequest(cfg, &v1.GetConfigRequest{ShowSensitive: *sensitive}))
	if err != nil {
		return err
	}
	var tree map[string]any
	decoder := json.NewDecoder(strings.NewReader(response.Msg.ConfigJson))
	decoder.UseNumber()
	if err := decoder.Decode(&tree); err != nil {
		return err
	}
	source, _ := tree["origins"].(map[string]any)
	delete(tree, "origins")
	var selected any = tree
	for _, part := range strings.Split(key, ".") {
		if key == "" {
			break
		}
		object, ok := selected.(map[string]any)
		if !ok {
			return configKeyNotFoundError{key}
		}
		selected, ok = object[part]
		if !ok || part == "" {
			return configKeyNotFoundError{key}
		}
	}
	selectedOrigins := map[string]any{}
	for name, origin := range source {
		if key == "" || name == key || strings.HasPrefix(name, key+".") {
			selectedOrigins[name] = origin
		}
	}
	if jsonOut {
		var result any = selected
		if *origins {
			result = map[string]any{"value": selected, "origins": selectedOrigins}
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, true, &v1.GetConfigResponse{ConfigJson: string(raw)})
	}
	if *keys {
		return printConfigKeys(cfg.Stdout, key, selected)
	}
	if key != "" {
		if text, ok := selected.(string); ok {
			fmt.Fprintln(cfg.Stdout, text)
		} else if _, object := selected.(map[string]any); object {
			if err := printConfigTree(cfg.Stdout, key, selected); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(cfg.Stdout, "%v\n", selected)
		}
	} else {
		if err := printConfigTree(cfg.Stdout, "", selected); err != nil {
			return err
		}
	}
	if *origins {
		names := make([]string, 0, len(selectedOrigins))
		for name := range selectedOrigins {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Fprintln(cfg.Stdout, "Origins: (not recorded)")
		} else {
			for _, name := range names {
				fmt.Fprintf(cfg.Stdout, "Origin: %s = %v\n", name, selectedOrigins[name])
			}
		}
	}
	return nil
}
