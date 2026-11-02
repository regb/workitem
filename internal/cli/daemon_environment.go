package cli

import (
	"sort"
	"strings"

	"github.com/regb/workitem/internal/direnv"
)

// daemonBootstrapEnvironment is the environment an auto-started daemon runs
// with: the caller's, without what direnv loaded for its directory and
// without item and terminal context, which would otherwise last for the
// daemon's lifetime.
func daemonBootstrapEnvironment(entries []string) ([]string, error) {
	values := map[string]string{}
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	values, err := direnv.Revert(values)
	if err != nil {
		return nil, err
	}
	result := []string{}
	for key, value := range values {
		if strings.HasPrefix(key, "WI_") || strings.HasPrefix(key, "DIRENV_") || key == "TMUX" || key == "TMUX_PANE" || key == "PWD" || key == "OLDPWD" {
			continue
		}
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result, nil
}
