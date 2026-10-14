package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver"
	"github.com/regb/workitem/internal/config"
)

type configDiscoverer interface {
	DiscoverConfig(context.Context) (protocol.ConfigDiscoveryResult, error)
	ValidateConfig(context.Context, string, json.RawMessage) error
}

var profileName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func runInitMain(ctx context.Context, args []string, path string, self string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer, jsonOut bool) int {
	if topic, requested := requestedHelpTopic(append([]string{"init"}, args...)); requested {
		printHelp(stdout, topic)
		return ExitOK
	}
	clients := map[string]configDiscoverer{}
	for name, client := range configuredDriverClients(config.Default(), self, env) {
		clients[name] = client
	}
	if err := initialize(ctx, args, path, clients, stdin, stdout, stderr, jsonOut); err != nil {
		fmt.Fprintln(stderr, err)
		return exitCode(err)
	}
	return ExitOK
}
func initialize(ctx context.Context, args []string, path string, clients map[string]configDiscoverer, stdin io.Reader, stdout, stderr io.Writer, jsonOut bool) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	selected := fs.String("default", "", "Detected profile to use as default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wi init [--default PROFILE]")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("configuration already exists at %s; edit it to configure default_agent", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	cfg := config.Default()
	names := make([]string, 0, len(clients))
	for name := range clients {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, driver := range names {
		discovered, err := clients[driver].DiscoverConfig(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "warning: %s discovery: %v\n", driver, err)
			continue
		}
		for _, warning := range discovered.Warnings {
			fmt.Fprintf(stderr, "warning: %s discovery: %s\n", driver, warning)
		}
		for _, candidate := range discovered.Profiles {
			if !profileName.MatchString(candidate.Name) || candidate.Name == "default" {
				return fmt.Errorf("driver %s discovered invalid profile name %q", driver, candidate.Name)
			}
			if _, exists := cfg.Agents[candidate.Name]; exists {
				return fmt.Errorf("duplicate discovered profile %q", candidate.Name)
			}
			values, err := discoveredValues(candidate.Config)
			if err != nil {
				return fmt.Errorf("driver %s discovered invalid configuration: %w", driver, err)
			}
			if err := clients[driver].ValidateConfig(ctx, candidate.Name, candidate.Config); err != nil {
				return fmt.Errorf("driver %s discovered invalid profile %s: %w", driver, candidate.Name, err)
			}
			cfg.Agents[candidate.Name] = config.AgentDefinitionConfig{Driver: driver, Label: candidate.Label, Config: values}
		}
	}
	if len(cfg.Agents) == 0 {
		return fmt.Errorf("no coding agents detected; install Pi or Claude Code on PATH, then run 'wi init' again")
	}
	profiles := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	if *selected == "" {
		if jsonOut {
			return fmt.Errorf("wi init --json requires --default PROFILE")
		}
		fmt.Fprintln(stderr, "Detected agents:")
		for i, name := range profiles {
			label := cfg.Agents[name].Label
			if label == "" {
				label = name
			}
			fmt.Fprintf(stderr, "  %d. %s (%s)\n", i+1, name, label)
		}
		reader := bufio.NewReader(stdin)
		for {
			fmt.Fprint(stderr, "Default agent (name or number): ")
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				return err
			}
			line = strings.TrimSpace(line)
			if line == "q" || line == "quit" {
				return fmt.Errorf("initialization canceled; no configuration written")
			}
			if number, e := strconv.Atoi(line); e == nil && number >= 1 && number <= len(profiles) {
				line = profiles[number-1]
			}
			if _, ok := cfg.Agents[line]; ok {
				*selected = line
				break
			}
			if err == io.EOF {
				return fmt.Errorf("default agent selection required; use 'wi init --default PROFILE' for noninteractive setup")
			}
			fmt.Fprintln(stderr, "Choose one of the detected profiles, or q to cancel.")
		}
	}
	_, ok := cfg.Agents[*selected]
	if !ok {
		return fmt.Errorf("default profile %q was not detected; choose %s", *selected, strings.Join(profiles, ", "))
	}
	cfg.DefaultAgent = *selected
	if err := config.Validate(cfg); err != nil {
		return err
	}
	data, err := initialConfig(cfg)
	if err != nil {
		return err
	}
	if err := writeInitialConfig(path, data); err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(stdout).Encode(map[string]any{"config_file": path, "default_agent": *selected, "profiles": profiles})
	}
	fmt.Fprintf(stdout, "Created %s\nDefault agent: %s\n", path, *selected)
	return nil
}
func discoveredValues(raw json.RawMessage) (map[string]any, error) {
	var values map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, fmt.Errorf("configuration must be an object")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("one configuration object required")
	}
	for key, value := range values {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(key) {
			return nil, fmt.Errorf("invalid configuration key %q", key)
		}
		if _, err := tomlValue(value); err != nil {
			return nil, err
		}
	}
	return values, nil
}
func tomlValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return strconv.Quote(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case json.Number:
		if _, err := strconv.Atoi(string(v)); err != nil {
			return "", fmt.Errorf("configuration integers must fit an int: %w", err)
		}
		return string(v), nil
	case []any:
		parts := make([]string, len(v))
		for i, entry := range v {
			s, ok := entry.(string)
			if !ok {
				return "", fmt.Errorf("configuration arrays must contain strings")
			}
			parts[i] = strconv.Quote(s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	default:
		return "", fmt.Errorf("unsupported discovered configuration value %T", value)
	}
}
func initialConfig(cfg config.Config) ([]byte, error) {
	var out strings.Builder
	out.WriteString("# Created by wi init. Agent profiles and executable paths are explicit.\n")
	fmt.Fprintf(&out, "default_agent = %s\n", strconv.Quote(cfg.DefaultAgent))
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		agent := cfg.Agents[name]
		fmt.Fprintf(&out, "\n[agents.%s]\ndriver = %s\n", name, strconv.Quote(agent.Driver))
		if agent.Label != "" {
			fmt.Fprintf(&out, "label = %s\n", strconv.Quote(agent.Label))
		}
		fmt.Fprintf(&out, "\n[agents.%s.config]\n", name)
		keys := make([]string, 0, len(agent.Config))
		for key := range agent.Config {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := tomlValue(agent.Config[key])
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&out, "%s = %s\n", key, value)
		}
	}
	return []byte(out.String()), nil
}
func writeInitialConfig(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".wi-init-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Link publishes the complete file atomically and never replaces a file
	// or symlink created by another setup process.
	if err := os.Link(file.Name(), path); err != nil {
		return fmt.Errorf("publish initial configuration: %w", err)
	}
	return nil
}

var _ configDiscoverer = agentdriver.Client{}
