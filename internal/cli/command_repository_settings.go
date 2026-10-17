package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/options"
	"github.com/regb/workitem/internal/plugins/bundled"
)

func runRepositorySettings(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{fmt.Errorf("usage: wi repo settings <show|set|unset> [NAME] [KEY [VALUE]]")}
	}
	action := args[0]
	positional := []string{}
	showSensitive := false
	literal := false
	for _, arg := range args[1:] {
		if arg == "--" && !literal {
			literal = true
			continue
		}
		if arg == "--show-sensitive" && !literal {
			showSensitive = true
			continue
		}
		if strings.HasPrefix(arg, "--") && !literal {
			return usageErr{fmt.Errorf("unknown repository settings option %q", arg)}
		}
		positional = append(positional, arg)
	}
	explicit, key, text := "", "", ""
	switch action {
	case "show":
		if len(positional) > 1 {
			return usageErr{fmt.Errorf("usage: wi repo settings show [NAME] [--show-sensitive]")}
		}
		if len(positional) == 1 {
			explicit = positional[0]
		}
	case "set":
		if len(positional) == 3 {
			explicit = positional[0]
			positional = positional[1:]
		}
		if len(positional) != 2 {
			return usageErr{fmt.Errorf("usage: wi repo settings set [NAME] plugins.PLUGIN.OPTION VALUE")}
		}
		key, text = positional[0], positional[1]
	case "unset":
		if len(positional) == 2 {
			explicit = positional[0]
			positional = positional[1:]
		}
		if len(positional) != 1 {
			return usageErr{fmt.Errorf("usage: wi repo settings unset [NAME] plugins.PLUGIN.OPTION")}
		}
		key = positional[0]
	default:
		return usageErr{fmt.Errorf("unknown repository settings action %q", action)}
	}
	if showSensitive && action != "show" {
		return usageErr{fmt.Errorf("--show-sensitive applies only to settings show")}
	}
	selector, err := repositorySelector(ctx, cfg, "settings "+action, explicit)
	if err != nil {
		return err
	}
	current, err := cfg.Coordinator.RepositorySettings(ctx, selector)
	if err != nil {
		return err
	}
	registry := bundled.Registry()
	if cfg.Plugins != nil {
		registry = cfg.Plugins.Registry.Options()
	}
	if action == "show" {
		global := config.Default()
		if cfg.GlobalConfig != nil {
			global = *cfg.GlobalConfig
		}
		effective, warnings, resolveErr := registry.ResolveRepository(global.PluginSettings, current.Settings.Namespaces)
		if resolveErr != nil {
			warnings = append(warnings, resolveErr.Error())
			effective = map[string]map[string]any{}
		}
		origins := global.PluginOrigins()
		for _, d := range registry.Definitions() {
			for _, o := range d.Options {
				if origins["plugins."+d.Name+"."+o.Name] == "" {
					origins["plugins."+d.Name+"."+o.Name] = "default"
				}
			}
		}
		for namespace, entries := range current.Settings.Namespaces {
			for key := range entries {
				origins[namespace+"."+key] = "repository:" + current.Repository.ID
			}
		}
		stored := current.Settings.Namespaces
		// Render only sanitized copies; wire values remain private to the host.
		if !showSensitive {
			stored = registry.RedactRepository(stored)
			tree := map[string]any{}
			for name, values := range effective {
				tree[name] = values
			}
			registry.Redact(tree)
		}
		namespacedEffective := map[string]map[string]any{}
		for name, values := range effective {
			namespacedEffective["plugins."+name] = values
		}
		result := struct {
			Repository model.OwnedRepository                 `json:"repository"`
			Revision   uint64                                `json:"revision"`
			Overrides  map[string]map[string]json.RawMessage `json:"overrides"`
			Effective  map[string]map[string]any             `json:"effective"`
			Origins    map[string]string                     `json:"origins"`
			Warnings   []string                              `json:"warnings"`
		}{current.Repository, current.Settings.Revision, stored, namespacedEffective, origins, warnings}
		if result.Warnings == nil {
			result.Warnings = []string{}
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		fmt.Fprintf(cfg.Stdout, "Repository: %s\nRevision: %d\nStored overrides:\n", current.Repository.Name, current.Settings.Revision)
		for _, warning := range warnings {
			fmt.Fprintln(cfg.Stderr, "warning: "+warning)
		}
		raw := map[string]any{}
		for namespace, entries := range stored {
			values := map[string]any{}
			for key, value := range entries {
				values[key] = value
			}
			raw[namespace] = values
		}
		if len(raw) == 0 {
			fmt.Fprintln(cfg.Stdout, "(none)")
		} else if err := printConfigTree(cfg.Stdout, "", raw); err != nil {
			return err
		}
		fmt.Fprintln(cfg.Stdout, "Effective plugin settings:")
		tree := map[string]any{}
		for name, entries := range effective {
			tree["plugins."+name] = entries
		}
		return printConfigTree(cfg.Stdout, "", tree)
	}
	namespace, option, err := options.SplitRepositoryKey(key)
	if err != nil {
		return usageErr{err}
	}
	var value json.RawMessage
	if action == "set" {
		value, err = registry.ParseRepositoryValue(namespace, option, text)
		if err != nil {
			return usageErr{err}
		}
	}
	id, err := model.NewID()
	if err != nil {
		return err
	}
	result, err := cfg.Coordinator.SetRepositorySetting(ctx, coordinator.SetRepositorySettingCommand{ID: "repository-setting:" + id, ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: current.Repository.ID, ExpectedRevision: current.Settings.Revision, Namespace: namespace, Key: option, Value: value, Unset: action == "unset", CreatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, result)
	}
	verb := "set"
	if action == "unset" {
		verb = "unset"
	}
	if !result.Changed {
		fmt.Fprintf(cfg.Stdout, "%s: %s unchanged\n", current.Repository.Name, key)
	} else {
		fmt.Fprintf(cfg.Stdout, "%s: %s %s\n", current.Repository.Name, key, verb)
	}
	return nil
}
