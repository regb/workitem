package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

const agentProfileUsage = "usage: wi agent profile <list|add|edit|remove|default|set> ..."

// runAgentProfiles manages the agent profiles the daemon keeps. set, which
// moves an item to another profile, is runAgentProfile.
func runAgentProfiles(ctx context.Context, action string, args []string, cfg Config, jsonOut bool) error {
	if cfg.Coordinator == nil {
		return errors.New("agent profiles require the daemon")
	}
	fs := newFlagSet("agent profile "+action, cfg.Stderr)
	var driver, label string
	labelSet := false
	if action == "add" {
		fs.StringVar(&driver, "driver", "", "the driver: pi or claude")
	}
	if action == "add" || action == "edit" {
		fs.Func("label", "the name shown for the profile", func(value string) error { label, labelSet = value, true; return nil })
	}
	positional := []string{}
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return usageErr{err}
		}
		if fs.NArg() == 0 {
			break
		}
		positional, rest = append(positional, fs.Arg(0)), fs.Args()[1:]
	}
	current, err := cfg.Coordinator.AgentProfiles(ctx)
	if err != nil {
		return err
	}
	command := coordinator.ChangeAgentProfileCommand{ExpectedRevision: &current.Revision}
	switch action {
	case "list":
		if len(positional) != 0 {
			return usageErr{errors.New("usage: wi agent profile list")}
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, current)
		}
		writer := tabwriter.NewWriter(cfg.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "NAME\tDRIVER\tDEFAULT\tLABEL\tSETTINGS")
		for _, profile := range current.Profiles {
			marker := ""
			if profile.Name == current.Default {
				marker = "*"
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", profile.Name, profile.Driver, marker, profile.Label, profileSettings(profile.Config))
		}
		return writer.Flush()
	case "add", "edit":
		if len(positional) == 0 {
			return usageErr{fmt.Errorf("usage: wi agent profile %s NAME %s[--label LABEL] [KEY=VALUE...]", action, map[bool]string{true: "--driver DRIVER ", false: ""}[action == "add"])}
		}
		profile, exists := current.Find(positional[0])
		if action == "add" {
			if exists {
				return fmt.Errorf("agent profile %s exists; change it with wi agent profile edit", positional[0])
			}
			if driver == "" {
				return usageErr{errors.New("wi agent profile add needs --driver")}
			}
			profile = model.AgentProfile{Name: positional[0], Driver: driver}
		} else if !exists {
			return fmt.Errorf("no agent profile %s", positional[0])
		}
		if labelSet {
			profile.Label = label
		}
		profile.Config = maps.Clone(profile.Config)
		if profile.Config == nil {
			profile.Config = map[string]any{}
		}
		// KEY=VALUE sets a driver setting; KEY= removes it.
		for _, arg := range positional[1:] {
			key, value, ok := strings.Cut(arg, "=")
			if !ok || key == "" {
				return usageErr{fmt.Errorf("setting %q must be KEY=VALUE", arg)}
			}
			if value == "" {
				delete(profile.Config, key)
			} else {
				profile.Config[key] = value
			}
		}
		if err := cfg.App.ValidateAgentProfile(ctx, profile); err != nil {
			return err
		}
		command.Put = &profile
	case "remove", "default":
		if len(positional) != 1 {
			return usageErr{fmt.Errorf("usage: wi agent profile %s NAME", action)}
		}
		if action == "remove" {
			command.Remove = positional[0]
		} else {
			command.Default = positional[0]
		}
	default:
		return usageErr{errors.New(agentProfileUsage)}
	}
	changed, err := cfg.Coordinator.ChangeAgentProfile(ctx, command)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, changed)
	}
	switch action {
	case "remove":
		fmt.Fprintf(cfg.Stdout, "removed agent profile %s\n", command.Remove)
		if users := cfg.App.AgentProfileUsers(command.Remove); users > 0 {
			fmt.Fprintf(cfg.Stdout, "%d open items still use it; each asks to move to another profile (wi agent profile set) when it next starts\n", users)
		}
	case "default":
		fmt.Fprintf(cfg.Stdout, "%s is the default agent profile\n", command.Default)
	default:
		fmt.Fprintf(cfg.Stdout, "saved agent profile %s; agents started from now on use it\n", command.Put.Name)
	}
	return nil
}

func profileSettings(config map[string]any) string {
	parts := []string{}
	for _, key := range sortedKeys(config) {
		parts = append(parts, fmt.Sprintf("%s=%v", key, config[key]))
	}
	return strings.Join(parts, " ")
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
