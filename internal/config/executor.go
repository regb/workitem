package config

import (
	"fmt"
	"strings"
)

func parseExecutorSetting(cfg *Config, section, key, value string) (bool, error) {
	if !strings.HasPrefix(section, "executors.") {
		return false, nil
	}
	parts := strings.Split(section, ".")
	if len(parts) != 2 || parts[1] == "" {
		return true, fmt.Errorf("expected [executors.<name>]")
	}
	profile := cfg.Executors[parts[1]]
	switch key {
	case "driver":
		profile.Driver = parseStringValue(value)
	case "image":
		profile.Image = parseStringValue(value)
	case "network":
		profile.Network = parseStringValue(value)
	case "host":
		profile.Host = parseStringValue(value)
	case "root":
		profile.Root = parseStringValue(value)
	case "ssh_config":
		profile.SSHConfig = parseStringValue(value)
	default:
		return true, fmt.Errorf("unknown executor setting %q", key)
	}
	cfg.Executors[parts[1]] = profile
	return true, nil
}
