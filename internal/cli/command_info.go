package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/xdg"
)

type infoValue struct {
	name     string
	jsonName string
	value    string
}

func infoCommandRequested(args []string) bool {
	filtered, _ := extractGlobals(args)
	return len(filtered) > 0 && filtered[0] == "info"
}

func runInfoMain(args []string, paths xdg.Paths, stdout, stderr io.Writer, jsonOut bool) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: wi info [key]")
		return ExitUsage
	}
	operatorSocket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitError
	}
	agentSocket, err := coordinator.AgentSocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitError
	}
	values := []infoValue{
		{name: "data-root", jsonName: "data_root", value: paths.DataRoot},
		{name: "database", jsonName: "database", value: filepath.Join(paths.DataRoot, "wi.db")},
		{name: "config-file", jsonName: "config_file", value: paths.ConfigFile},
		{name: "cache-root", jsonName: "cache_root", value: paths.CacheRoot},
		{name: "state-root", jsonName: "state_root", value: paths.DataStateRoot},
		{name: "runtime-root", jsonName: "runtime_root", value: paths.DataRuntimeRoot},
		{name: "operator-socket", jsonName: "operator_socket", value: operatorSocket},
		{name: "agent-socket", jsonName: "agent_socket", value: agentSocket},
	}
	if len(args) == 1 {
		for _, entry := range values {
			if args[0] != entry.name {
				continue
			}
			if jsonOut {
				if err := writeJSON(stdout, map[string]string{entry.jsonName: entry.value}); err != nil {
					fmt.Fprintln(stderr, err)
					return ExitError
				}
			} else {
				fmt.Fprintln(stdout, entry.value)
			}
			return ExitOK
		}
		fmt.Fprintf(stderr, "unknown info key %q\n", args[0])
		return ExitUsage
	}
	if jsonOut {
		result := make(map[string]string, len(values))
		for _, entry := range values {
			result[entry.jsonName] = entry.value
		}
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintln(stderr, err)
			return ExitError
		}
		return ExitOK
	}
	for _, entry := range values {
		fmt.Fprintf(stdout, "%s: %s\n", entry.name, entry.value)
	}
	return ExitOK
}
