package cli

import (
	"sort"

	"github.com/regb/workitem/internal/agentdriver"
	"github.com/regb/workitem/internal/config"
)

func configuredDriverClients(cfg config.Config, selfPath string, environment map[string]string) map[string]agentdriver.Client {
	clients := map[string]agentdriver.Client{}
	env := make([]string, 0, len(environment))
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+environment[key])
	}
	for driverName, definition := range cfg.Drivers {
		path, args := definition.Command, []string(nil)
		if path == "" && (driverName == "pi" || driverName == "claude") && selfPath != "" {
			path, args = selfPath, []string{"internal-driver", driverName}
		}
		if path != "" {
			clients[driverName] = agentdriver.Client{Driver: driverName, Path: path, Args: args, Env: env}
		}
	}
	return clients
}
