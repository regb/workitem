package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/priority"
)

const (
	DefaultMaxDeepWorkActive = 2
	DefaultRepositoryFolders = 2
)

type Config struct {
	DeepWork    DeepWorkConfig                    `json:"deep_work"`
	Item        ItemConfig                        `json:"item"`
	List        ListConfig                        `json:"list"`
	AgentStatus AgentStatusConfig                 `json:"agent_status"`
	Attention   AttentionConfig                   `json:"attention"`
	Direnv      DirenvConfig                      `json:"direnv"`
	Agents      map[string]AgentDefinitionConfig  `json:"agents"`
	Drivers     map[string]DriverDefinitionConfig `json:"drivers"`
	Executors   map[string]ExecutorConfig         `json:"executors,omitempty"`
}

type AgentDefinitionConfig struct {
	Driver string         `json:"driver"`
	Config map[string]any `json:"config,omitempty"`
}

type DriverDefinitionConfig struct {
	Command  string         `json:"command,omitempty"`
	Defaults map[string]any `json:"defaults,omitempty"`
}

type ExecutorConfig struct {
	Transport   string                         `json:"transport"`
	Destination string                         `json:"destination"`
	SSHCommand  string                         `json:"ssh_command,omitempty"`
	WICommand   string                         `json:"wi_command,omitempty"`
	Shell       string                         `json:"shell,omitempty"`
	ShellConfig string                         `json:"shell_config,omitempty"`
	TmuxConfig  string                         `json:"tmux_config,omitempty"`
	ForwardEnv  []string                       `json:"forward_env,omitempty"`
	Agents      map[string]ExecutorAgentConfig `json:"agents,omitempty"`
}

type ExecutorAgentConfig struct {
	Config map[string]any `json:"config,omitempty"`
}

type ProjectConfig struct {
	Item ItemConfig `json:"item"`
}

type ItemConfig struct {
	Defaults ItemDefaultsConfig `json:"defaults"`
}

type ItemDefaultsConfig struct {
	Labels []string `json:"labels,omitempty"`
}

type DeepWorkConfig struct {
	MaxActive int `json:"max_active"`
}

type ListConfig struct {
	RepositoryFolders int      `json:"repository_folders"`
	Labels            []string `json:"labels,omitempty"`
}

type AttentionConfig struct {
	Priority string `json:"priority"`
}

type AgentStatusConfig struct {
	Markers AgentStatusMarkersConfig `json:"markers"`
}

type AgentStatusMarkersConfig struct {
	Busy    string `json:"busy,omitempty"`
	Idle    string `json:"idle,omitempty"`
	Problem string `json:"problem,omitempty"`
}

type DirenvConfig struct {
	// AutoTrustRepositories is a user-configured allowlist. Repository config
	// must never be able to opt itself into execution trust.
	AutoTrustRepositories []string `json:"auto_trust_repositories,omitempty"`
}

func Default() Config {
	return Config{DeepWork: DeepWorkConfig{MaxActive: DefaultMaxDeepWorkActive}, List: ListConfig{RepositoryFolders: DefaultRepositoryFolders}, Attention: AttentionConfig{Priority: priority.DefaultStrategy}, Agents: map[string]AgentDefinitionConfig{"default": {Driver: "pi", Config: map[string]any{}}}, Drivers: map[string]DriverDefinitionConfig{"pi": {Defaults: map[string]any{}}}, Executors: map[string]ExecutorConfig{}}
}

func Load(path string) (Config, []string, error) {
	cfg := Default()
	if strings.TrimSpace(path) == "" {
		return cfg, nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil, nil
		}
		return cfg, nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	section := ""
	warnings := []string{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := stripComment(strings.TrimSpace(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			warnings = append(warnings, fmt.Sprintf("line %d ignored: expected key = value", lineNo))
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if handled, err := parseAgentDriverSetting(&cfg, section, key, value); handled {
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse %s.%s on line %d: %w", section, key, lineNo, err)
			}
			continue
		}
		if handled, err := parseExecutorSetting(&cfg, section, key, value); handled {
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse %s.%s on line %d: %w", section, key, lineNo, err)
			}
			continue
		}
		switch section + "." + key {
		case "deep_work.max_active":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse deep_work.max_active on line %d: %w", lineNo, err)
			}
			cfg.DeepWork.MaxActive = parsed
		case "item.defaults.labels":
			parsed, err := parseStringArray(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse item.defaults.labels on line %d: %w", lineNo, err)
			}
			cfg.Item.Defaults.Labels = parsed
		case "list.repository_folders":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse list.repository_folders on line %d: %w", lineNo, err)
			}
			cfg.List.RepositoryFolders = parsed
		case "list.labels":
			parsed, err := parseStringArray(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse list.labels on line %d: %w", lineNo, err)
			}
			cfg.List.Labels = parsed
		case "attention.priority":
			cfg.Attention.Priority = parseStringValue(value)
		case "agent_status.markers.busy":
			cfg.AgentStatus.Markers.Busy = parseStringValue(value)
		case "agent_status.markers.idle":
			cfg.AgentStatus.Markers.Idle = parseStringValue(value)
		case "agent_status.markers.problem":
			cfg.AgentStatus.Markers.Problem = parseStringValue(value)
		case "direnv.auto_trust_repositories":
			parsed, err := parseStringArray(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse direnv.auto_trust_repositories on line %d: %w", lineNo, err)
			}
			cfg.Direnv.AutoTrustRepositories = parsed
		default:
			warnings = append(warnings, fmt.Sprintf("line %d ignored: unknown key %s.%s", lineNo, section, key))
		}
	}
	if err := scanner.Err(); err != nil {
		return cfg, warnings, fmt.Errorf("read config: %w", err)
	}
	return cfg, warnings, Validate(cfg)
}

func Validate(cfg Config) error {
	if cfg.DeepWork.MaxActive < 0 {
		return fmt.Errorf("deep_work.max_active must be nonnegative")
	}
	if _, err := model.NormalizeLabels(cfg.Item.Defaults.Labels); err != nil {
		return fmt.Errorf("item.defaults.labels: %w", err)
	}
	if cfg.List.RepositoryFolders < 1 {
		return fmt.Errorf("list.repository_folders must be at least 1")
	}
	if strings.TrimSpace(cfg.Attention.Priority) == "" {
		return fmt.Errorf("attention.priority must not be empty")
	}
	if _, err := priority.Rank(cfg.Attention.Priority, nil); err != nil {
		return err
	}
	defaultAgent, ok := cfg.Agents["default"]
	if !ok || strings.TrimSpace(defaultAgent.Driver) == "" {
		return fmt.Errorf("agents.default must select a driver")
	}
	for name, definition := range cfg.Agents {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(definition.Driver) == "" {
			return fmt.Errorf("agent definitions require a name and driver")
		}
		if _, ok := cfg.Drivers[definition.Driver]; !ok {
			return fmt.Errorf("agent %q selects unconfigured driver %q", name, definition.Driver)
		}
	}
	for name, executor := range cfg.Executors {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("executor definitions require a name")
		}
		if executor.Transport != "ssh" {
			return fmt.Errorf("executor %q has unsupported transport %q; expected ssh", name, executor.Transport)
		}
		if destination := strings.TrimSpace(executor.Destination); destination == "" {
			return fmt.Errorf("executor %q requires destination", name)
		} else if strings.HasPrefix(destination, "-") || strings.ContainsAny(destination, " \t") {
			return fmt.Errorf("executor %q destination must be one SSH destination without options", name)
		}
		for _, value := range []string{executor.Destination, executor.SSHCommand, executor.WICommand, executor.Shell} {
			if strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("executor %q contains an invalid command or destination", name)
			}
		}
		for _, key := range executor.ForwardEnv {
			if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\r\n\x00") {
				return fmt.Errorf("executor %q contains invalid forward_env key %q", name, key)
			}
		}
	}
	for _, repository := range cfg.Direnv.AutoTrustRepositories {
		if !filepath.IsAbs(strings.TrimSpace(repository)) {
			return fmt.Errorf("direnv.auto_trust_repositories entries must be absolute paths: %q", repository)
		}
	}
	return nil
}

func parseExecutorSetting(cfg *Config, section, key, value string) (bool, error) {
	parts := strings.Split(section, ".")
	if len(parts) < 2 || parts[0] != "executors" {
		return false, nil
	}
	name := strings.TrimSpace(parts[1])
	executor := cfg.Executors[name]
	if executor.Agents == nil {
		executor.Agents = map[string]ExecutorAgentConfig{}
	}
	if len(parts) == 5 && parts[2] == "agents" && parts[4] == "config" {
		agentName := strings.TrimSpace(parts[3])
		agent := executor.Agents[agentName]
		if agent.Config == nil {
			agent.Config = map[string]any{}
		}
		parsed, err := parseDriverValue(value)
		if err != nil {
			return true, err
		}
		agent.Config[key] = parsed
		executor.Agents[agentName] = agent
		cfg.Executors[name] = executor
		return true, nil
	}
	if len(parts) != 2 {
		return false, nil
	}
	switch key {
	case "transport":
		executor.Transport = parseStringValue(value)
	case "destination":
		executor.Destination = parseStringValue(value)
	case "ssh_command":
		executor.SSHCommand = parseStringValue(value)
	case "wi_command":
		executor.WICommand = parseStringValue(value)
	case "shell":
		executor.Shell = parseStringValue(value)
	case "shell_config":
		executor.ShellConfig = parseStringValue(value)
	case "tmux_config":
		executor.TmuxConfig = parseStringValue(value)
	case "forward_env":
		values, err := parseStringArray(value)
		if err != nil {
			return true, err
		}
		executor.ForwardEnv = values
	default:
		return false, nil
	}
	if executor.Transport == "" {
		executor.Transport = "ssh"
	}
	cfg.Executors[name] = executor
	return true, nil
}

func parseAgentDriverSetting(cfg *Config, section, key, value string) (bool, error) {
	parts := strings.Split(section, ".")
	if len(parts) < 2 {
		return false, nil
	}
	switch parts[0] {
	case "agents":
		name := strings.TrimSpace(parts[1])
		definition := cfg.Agents[name]
		if len(parts) == 2 && key == "driver" {
			definition.Driver = parseStringValue(value)
			if definition.Config == nil {
				definition.Config = map[string]any{}
			}
			cfg.Agents[name] = definition
			return true, nil
		}
		if len(parts) == 3 && parts[2] == "config" {
			parsed, err := parseDriverValue(value)
			if err != nil {
				return true, err
			}
			if definition.Config == nil {
				definition.Config = map[string]any{}
			}
			definition.Config[key] = parsed
			cfg.Agents[name] = definition
			return true, nil
		}
	case "drivers":
		name := strings.TrimSpace(parts[1])
		driver := cfg.Drivers[name]
		if len(parts) == 2 && key == "command" {
			driver.Command = parseStringValue(value)
			if driver.Defaults == nil {
				driver.Defaults = map[string]any{}
			}
			cfg.Drivers[name] = driver
			return true, nil
		}
		if len(parts) == 3 && parts[2] == "defaults" {
			parsed, err := parseDriverValue(value)
			if err != nil {
				return true, err
			}
			if driver.Defaults == nil {
				driver.Defaults = map[string]any{}
			}
			driver.Defaults[key] = parsed
			cfg.Drivers[name] = driver
			return true, nil
		}
	}
	return false, nil
}

func parseDriverValue(value string) (any, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") {
		return parseStringArray(value)
	}
	if value == "true" || value == "false" {
		return value == "true", nil
	}
	if len(value) >= 2 && value[0] == '"' {
		return parseStringValue(value), nil
	}
	if number, err := strconv.Atoi(value); err == nil {
		return number, nil
	}
	return nil, fmt.Errorf("expected a string, string array, boolean, or integer")
}

func parseStringArray(s string) ([]string, error) {
	raw := strings.TrimSpace(s)
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return nil, fmt.Errorf("expected an array of quoted strings")
	}
	body := strings.TrimSpace(raw[1 : len(raw)-1])
	body = strings.TrimSpace(strings.TrimSuffix(body, ","))
	raw = "[" + body + "]"
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("expected an array of quoted strings: %w", err)
	}
	return values, nil
}

func parseStringValue(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if unquoted, err := strconv.Unquote(s); err == nil {
			return unquoted
		}
	}
	return s
}

func stripComment(s string) string {
	inQuote := false
	for i, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}
