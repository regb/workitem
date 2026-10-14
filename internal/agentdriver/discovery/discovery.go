// Package discovery provides read-only helpers for bundled driver discovery.
package discovery

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// Executable detects a program on PATH without invoking it. Preserve the
// launcher symlink so updating an installation does not invalidate the config.
func Executable(name, label, binary string) (protocol.ConfigDiscoveryResult, error) {
	result := protocol.ConfigDiscoveryResult{Profiles: []protocol.DiscoveredAgentConfig{}}
	path, err := exec.LookPath(binary)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return result, nil
		}
		return result, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return result, err
	}
	config, err := json.Marshal(map[string]string{"executable": path})
	if err != nil {
		return result, err
	}
	result.Profiles = append(result.Profiles, protocol.DiscoveredAgentConfig{Name: name, Label: label, Config: config})
	return result, nil
}
