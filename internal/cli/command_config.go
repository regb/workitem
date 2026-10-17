package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/plugins/bundled"
)

type configKeyNotFoundError struct{ key string }

func (e configKeyNotFoundError) Error() string {
	return fmt.Sprintf("unknown configuration key %q", e.key)
}

// Configuration inspection is independent of the app, daemon, repository
// environment, and driver processes.
func runConfigMain(args []string, path string, stdout, stderr io.Writer, jsonOut bool) int {
	if err := queryConfig(args, path, stdout, stderr, jsonOut); err != nil {
		fmt.Fprintln(stderr, err)
		return exitCode(err)
	}
	return ExitOK
}

func queryConfig(args []string, path string, stdout, stderr io.Writer, jsonOut bool) error {
	showSensitive := false
	positional := []string{}
	showOrigins := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--repo" {
			return usageErr{fmt.Errorf("repository policy is stored by the daemon; use wi repo settings show NAME")}
		}
		if arg == "--origins" {
			showOrigins = true
			continue
		}
		if arg == "--show-sensitive" {
			showSensitive = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return usageErr{fmt.Errorf("unknown config option %q", arg)}
		}
		positional = append(positional, arg)
	}
	if len(positional) == 0 {
		positional = []string{"show"}
	}
	command := positional[0]
	if (command == "get" && (len(positional) != 2 || strings.TrimSpace(positional[1]) == "")) || (command != "get" && len(positional) != 1) {
		return usageErr{fmt.Errorf("usage: wi config <show|get KEY|agents|drivers|plugins> [--origins] [--show-sensitive]")}
	}
	if command != "show" && command != "get" && command != "agents" && command != "drivers" && command != "plugins" {
		return usageErr{fmt.Errorf("unknown config command %q", command)}
	}
	self, _ := os.Executable()
	cfg, pluginHost, warnings, err := bundled.Load(path, self)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "warning: config: %s\n", warning)
	}
	if showOrigins && (command == "agents" || command == "plugins") {
		return usageErr{fmt.Errorf("--origins applies to configuration value queries, not profile/schema summaries")}
	}
	if command == "agents" {
		return printConfigAgents(stdout, cfg, jsonOut)
	}
	if command == "plugins" {
		definitions := pluginHost.Registry.Options().Definitions()
		if !showSensitive {
			for i := range definitions {
				for j := range definitions[i].Options {
					if definitions[i].Options[j].Secret {
						definitions[i].Options[j].Default = "[redacted]"
					}
				}
			}
		}
		return writeJSON(stdout, definitions)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	var tree map[string]any
	if err := decodeConfigTree(raw, &tree); err != nil {
		return err
	}
	// Optional JSON fields still represent queryable settings when unset.
	tree["item"].(map[string]any)["defaults"].(map[string]any)["labels"] = cfg.Item.Defaults.Labels
	for name, profile := range cfg.Agents {
		definition := tree["agents"].(map[string]any)[name].(map[string]any)
		definition["label"] = profile.Label
		if _, ok := definition["config"]; !ok {
			definition["config"] = map[string]any{}
		}
	}
	for name, driver := range cfg.Drivers {
		definition := tree["drivers"].(map[string]any)[name].(map[string]any)
		definition["command"] = driver.Command
		if _, ok := definition["defaults"]; !ok {
			definition["defaults"] = map[string]any{}
		}
	}
	// Each [plugins.NAME] table shows its registration fields and options together.
	settings := map[string]any{}
	if raw, err := json.Marshal(cfg.PluginSettings); err != nil {
		return err
	} else if err := decodeConfigTree(raw, &settings); err != nil {
		return err
	}
	if !showSensitive {
		redactConfigDriverSettings(tree)
		pluginHost.Registry.Options().Redact(settings)
	}
	plugins, _ := tree["plugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}
	for name, values := range settings {
		table, _ := plugins[name].(map[string]any)
		if table == nil {
			table = map[string]any{}
		}
		for key, value := range values.(map[string]any) {
			table[key] = value
		}
		plugins[name] = table
	}
	tree["plugins"] = plugins
	var value any = tree
	key := ""
	if command == "drivers" {
		key = "drivers"
	} else if command == "get" {
		key = positional[1]
	}
	if key != "" {
		for _, part := range strings.Split(key, ".") {
			object, ok := value.(map[string]any)
			if !ok {
				return configKeyNotFoundError{key}
			}
			value, ok = object[part]
			if !ok || part == "" {
				return configKeyNotFoundError{key}
			}
		}
	}
	if showOrigins {
		origins := map[string]string{}
		for path, origin := range cfg.PluginOrigins() {
			if key == "" || path == key || strings.HasPrefix(path, key+".") {
				origins[path] = origin
			}
		}
		return writeJSON(stdout, struct {
			Value   any               `json:"value"`
			Origins map[string]string `json:"origins"`
		}{value, origins})
	}
	if jsonOut {
		return writeJSON(stdout, value)
	}
	if _, ok := value.(map[string]any); ok {
		return printConfigTree(stdout, key, value)
	}
	if text, ok := value.(string); ok {
		_, err = fmt.Fprintln(stdout, text)
		return err
	}
	return writeJSON(stdout, value)
}

// Driver settings and executable command lines are opaque. Plugin behavior
// options are redacted separately through their shared registered schemas.
func redactConfigDriverSettings(tree map[string]any) {
	for _, section := range []string{"agents", "drivers", "plugins"} {
		definitions, _ := tree[section].(map[string]any)
		for _, value := range definitions {
			definition, _ := value.(map[string]any)
			field := "config"
			if section == "drivers" {
				field = "defaults"
				if command, ok := definition["command"].(string); ok && command != "" {
					definition["command"] = "[redacted]"
				}
			}
			if section == "plugins" {
				if command, ok := definition["command"]; ok {
					definition["command"] = redactConfigValue(command)
				}
				continue
			}
			if settings, ok := definition[field]; ok {
				definition[field] = redactConfigValue(settings)
			}
		}
	}
}

func redactConfigValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, entry := range v {
			v[key] = redactConfigValue(entry)
		}
		return v
	case []any:
		for i, entry := range v {
			v[i] = redactConfigValue(entry)
		}
		return v
	case nil:
		return nil
	default:
		return "[redacted]"
	}
}

func printConfigTree(w io.Writer, prefix string, value any) error {
	if object, ok := value.(map[string]any); ok && len(object) > 0 {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if err := printConfigTree(w, path, object[key]); err != nil {
				return err
			}
		}
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s = %s\n", prefix, encoded)
	return err
}

type configAgentProfile struct {
	Name    string `json:"name"`
	Driver  string `json:"driver"`
	Label   string `json:"label,omitempty"`
	Default bool   `json:"default"`
}

func printConfigAgents(w io.Writer, cfg config.Config, jsonOut bool) error {
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	profiles := make([]configAgentProfile, 0, len(names))
	for _, name := range names {
		definition := cfg.Agents[name]
		profiles = append(profiles, configAgentProfile{Name: name, Driver: definition.Driver, Label: definition.Label, Default: name == cfg.DefaultAgent})
	}
	if jsonOut {
		return writeJSON(w, struct {
			DefaultAgent string               `json:"default_agent"`
			Agents       []configAgentProfile `json:"agents"`
		}{cfg.DefaultAgent, profiles})
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tDRIVER\tDEFAULT\tLABEL")
	for _, profile := range profiles {
		marker := ""
		if profile.Default {
			marker = "*"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", profile.Name, profile.Driver, marker, profile.Label)
	}
	return table.Flush()
}

func decodeConfigTree(raw []byte, target *map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}
