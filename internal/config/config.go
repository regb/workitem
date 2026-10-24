package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/options"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/priority"
)

const (
	DefaultMaxDeepWorkActive  = 2
	DefaultRepositoryFolders  = 2
	DefaultAppendSystemPrompt = "This conversation belongs to a Wi work item. Read its task with `wi show` and inspect task context with `wi show --json`. File unrelated work as a separate item; do not change lifecycle, workspaces, or terminals without the user's authorization."
	DefaultStartupMessage     = "Read this work item's description with `wi show`. Work on the task it describes. Run relevant checks and report the outcome."
)

type Config struct {
	DefaultAgent string                            `json:"default_agent"`
	DeepWork     DeepWorkConfig                    `json:"deep_work"`
	Item         ItemConfig                        `json:"item"`
	List         ListConfig                        `json:"list"`
	Agent        AgentConfig                       `json:"agent"`
	AgentStatus  AgentStatusConfig                 `json:"agent_status"`
	Attention    AttentionConfig                   `json:"attention"`
	Daemon       DaemonConfig                      `json:"daemon"`
	Agents       map[string]AgentDefinitionConfig  `json:"agents"`
	Drivers      map[string]DriverDefinitionConfig `json:"drivers"`
	Plugins      map[string]plugin.Registration    `json:"plugins,omitempty"`
	// PluginSettings holds the registered options from [plugins.NAME] tables,
	// resolved with schema defaults. Registration fields are in Plugins.
	PluginSettings map[string]map[string]any `json:"-"`
	optionOrigins  map[string]string
}

// DaemonConfig says how the coordinator daemon runs. Managed means a service
// manager such as systemd or launchd owns it: wi never starts, stops or
// replaces it, and refuses a client of another build instead of replacing
// the daemon; the service manager restarts it.
type DaemonConfig struct {
	Managed bool `json:"managed" toml:"managed"`
}

type AgentConfig struct {
	AppendSystemPrompt string `json:"append_system_prompt"`
	StartupMessage     string `json:"startup_message"`
}
type AgentDefinitionConfig struct {
	Driver string         `json:"driver"`
	Label  string         `json:"label,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}
type DriverDefinitionConfig struct {
	Command  string         `json:"command,omitempty"`
	Defaults map[string]any `json:"defaults,omitempty"`
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
	Labels            []string `json:"labels"`
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

func Default() Config {
	return Config{Agent: AgentConfig{StartupMessage: DefaultStartupMessage, AppendSystemPrompt: DefaultAppendSystemPrompt}, DeepWork: DeepWorkConfig{MaxActive: DefaultMaxDeepWorkActive}, List: ListConfig{RepositoryFolders: DefaultRepositoryFolders}, Attention: AttentionConfig{Priority: priority.DefaultStrategy}, Agents: map[string]AgentDefinitionConfig{}, Drivers: map[string]DriverDefinitionConfig{"pi": {Defaults: map[string]any{}}, "claude": {Defaults: map[string]any{}}}, PluginSettings: map[string]map[string]any{}}
}

// PluginRegistrations reads only installation metadata to build a declarative
// catalog before validating behavior settings. It never launches plugin code.
func PluginRegistrations(path string) (map[string]plugin.Registration, error) {
	root := map[string]any{}
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			if err := toml.Unmarshal(data, &root); err != nil {
				return nil, err
			}
		}
	}
	raw, err := json.Marshal(root["plugins"])
	if err != nil {
		return nil, err
	}
	var registrations map[string]plugin.Registration
	if err := json.Unmarshal(raw, &registrations); err != nil {
		return nil, err
	}
	return registrations, nil
}
func Load(path string) (Config, []string, error) { return LoadWithRegistry(path, nil) }

// LoadDaemon reads only the [daemon] table, so that daemon commands follow it
// whatever the state of the rest of the configuration. A missing file is
// the default: an unmanaged daemon.
func LoadDaemon(path string) (DaemonConfig, error) {
	var file struct {
		Daemon DaemonConfig `toml:"daemon"`
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) || strings.TrimSpace(path) == "" {
		return DaemonConfig{}, nil
	}
	if err != nil {
		return DaemonConfig{}, fmt.Errorf("open config: %w", err)
	}
	if err := toml.Unmarshal(data, &file); err != nil {
		return DaemonConfig{}, fmt.Errorf("parse config: %w", err)
	}
	return file.Daemon, nil
}
func LoadWithRegistry(path string, registry *options.Registry) (Config, []string, error) {
	cfg := Default()
	root := map[string]any{}
	warnings := []string{}
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return cfg, warnings, fmt.Errorf("open config: %w", err)
		}
		if err == nil {
			if err := toml.Unmarshal(data, &root); err != nil {
				return cfg, warnings, fmt.Errorf("parse config: %w", err)
			}
		}
	}
	for _, definition := range registry.Definitions() {
		for _, section := range definition.RetiredSections {
			if _, exists := root[section]; exists {
				return cfg, warnings, fmt.Errorf("[%s] is no longer supported; configure [plugins.%s] global defaults and wi repo settings explicitly", section, definition.Name)
			}
		}
	}
	if _, exists := root["extensions"]; exists {
		return cfg, warnings, fmt.Errorf("[extensions.NAME] has been merged into [plugins.NAME]; move those options into the plugin's table")
	}
	// Validate the typed core and preserve plugin options for registered schemas.
	known := map[string]map[string]bool{
		"deep_work": {"max_active": true}, "item": {"defaults": true}, "list": {"repository_folders": true, "labels": true}, "agent": {"startup_message": true, "append_system_prompt": true}, "agent_status": {"markers": true}, "attention": {"priority": true},
		"daemon": {"managed": true},
	}
	for section, value := range root {
		if keys, ok := known[section]; ok {
			if entries, ok := value.(map[string]any); ok {
				for key := range entries {
					if !keys[key] {
						warnings = append(warnings, "ignored unknown key "+section+"."+key)
					}
				}
			}
			continue
		}
		switch section {
		case "default_agent", "agents", "drivers", "plugins", "repositories":
		default:
			warnings = append(warnings, "ignored unknown key "+section)
		}
	}
	if _, exists := root["repositories"]; exists {
		return cfg, warnings, fmt.Errorf("repository overrides no longer belong in config.toml; use wi repo settings set NAME plugins.PLUGIN.OPTION VALUE explicitly")
	}
	data, err := json.Marshal(root)
	if err != nil {
		return cfg, warnings, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, warnings, fmt.Errorf("decode config: %w", err)
	}
	for name, d := range cfg.Agents {
		for key, value := range d.Config {
			d.Config[key] = normalizeArray(value)
		}
		cfg.Agents[name] = d
	}
	for name, d := range cfg.Drivers {
		for key, value := range d.Defaults {
			d.Defaults[key] = normalizeArray(value)
		}
		cfg.Drivers[name] = d
	}
	if err := Validate(cfg); err != nil {
		return cfg, warnings, err
	}
	cfg.PluginSettings, err = pluginSettings(root["plugins"])
	if err != nil {
		return cfg, warnings, err
	}
	w, err := registry.Validate(cfg.PluginSettings, false)
	warnings = append(warnings, w...)
	if err != nil {
		return cfg, warnings, err
	}
	cfg.optionOrigins = map[string]string{}
	for _, d := range registry.Definitions() {
		for _, o := range d.Options {
			cfg.optionOrigins["plugins."+d.Name+"."+o.Name] = "default"
		}
	}
	for name, values := range cfg.PluginSettings {
		for key := range values {
			cfg.optionOrigins["plugins."+name+"."+key] = "user"
		}
	}
	cfg.PluginSettings = registry.Resolve(cfg.PluginSettings, nil)
	sort.Strings(warnings)
	return cfg, warnings, nil
}

// pluginSettings takes the option keys of each [plugins.NAME] table, leaving
// the registration fields to plugin.Registration.
func pluginSettings(raw any) (map[string]map[string]any, error) {
	settings := map[string]map[string]any{}
	if raw == nil {
		return settings, nil
	}
	tables, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("plugins must be a table of [plugins.NAME] tables")
	}
	for name, value := range tables {
		table, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("plugins.%s must be a table", name)
		}
		for key, option := range table {
			if plugin.RegistrationKeys[key] {
				continue
			}
			if settings[name] == nil {
				settings[name] = map[string]any{}
			}
			settings[name][key] = option
		}
	}
	// Decode like the rest of the configuration, keeping JSON numbers.
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	settings = map[string]map[string]any{}
	if err := decoder.Decode(&settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func (cfg Config) PluginOrigins() map[string]string {
	result := map[string]string{}
	for key, value := range cfg.optionOrigins {
		result[key] = value
	}
	return result
}

func normalizeArray(value any) any {
	if entries, ok := value.([]any); ok {
		stringsOnly := make([]string, len(entries))
		for i, entry := range entries {
			text, ok := entry.(string)
			if !ok {
				return value
			}
			stringsOnly[i] = text
		}
		return stringsOnly
	}
	return value
}

func (cfg Config) ResolvedPluginSettings(registry *options.Registry) map[string]map[string]any {
	return registry.Resolve(cfg.PluginSettings, nil)
}
func Validate(cfg Config) error {
	if strings.ContainsRune(cfg.Agent.AppendSystemPrompt, 0) {
		return fmt.Errorf("agent.append_system_prompt must not contain NUL")
	}
	if strings.TrimSpace(cfg.Agent.StartupMessage) == "" {
		return fmt.Errorf("agent.startup_message must not be empty")
	}
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
	if strings.TrimSpace(cfg.DefaultAgent) == "" {
		return fmt.Errorf("default_agent must name an agent profile; run 'wi init' to create your configuration, or set default_agent in the existing file")
	}
	if _, ok := cfg.Agents[cfg.DefaultAgent]; !ok {
		return fmt.Errorf("default_agent references unconfigured profile %q", cfg.DefaultAgent)
	}
	if _, ok := cfg.Agents["default"]; ok {
		return fmt.Errorf("agents.default is no longer supported; rename it and set default_agent to its profile name")
	}
	for name, d := range cfg.Agents {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(d.Driver) == "" {
			return fmt.Errorf("agent definitions require a name and driver")
		}
		if len([]rune(d.Label)) > 80 {
			return fmt.Errorf("agent %q label is too long", name)
		}
		if _, ok := cfg.Drivers[d.Driver]; !ok {
			return fmt.Errorf("agent %q selects unconfigured driver %q", name, d.Driver)
		}
	}
	for name, plugin := range cfg.Plugins {
		if _, err := options.New(options.Definition{Name: name}); err != nil {
			return fmt.Errorf("plugin name %q must be lowercase letters, digits, or '-'", name)
		}
		if len(plugin.Command) > 0 && strings.TrimSpace(plugin.Command[0]) == "" {
			return fmt.Errorf("plugins.%s.command must name an executable", name)
		}
	}
	return nil
}
