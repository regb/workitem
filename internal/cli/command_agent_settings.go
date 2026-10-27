package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
)

func runAgentSettings(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "show"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet("agent settings "+action, cfg.Stderr)
	var item string
	var restart bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if action == "set" {
		fs.BoolVar(&restart, "restart", false, "restart a runtime that cannot apply a change live, on the same conversation")
	}
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	resolve := app.ResolveOptions{Selector: item, CWD: cfg.CWD, Env: cfg.Env}
	switch action {
	case "show":
		if fs.NArg() != 0 {
			return usageErr{errors.New("usage: wi agent settings [show] [--item <selector>]")}
		}
		view, err := cfg.App.AgentSettings(ctx, resolve)
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, view)
		}
		printAgentSettings(cfg, view)
		return nil
	case "set":
		changes := map[string]string{}
		for _, arg := range fs.Args() {
			name, value, ok := strings.Cut(arg, "=")
			if !ok || name == "" {
				return usageErr{fmt.Errorf("setting %q must be NAME=VALUE; an empty VALUE resets it to the profile", arg)}
			}
			changes[name] = value
		}
		if len(changes) == 0 {
			return usageErr{errors.New("usage: wi agent settings set [--item <selector>] [--restart] NAME=VALUE...")}
		}
		result, err := cfg.App.SetAgentSettings(ctx, app.SetAgentSettingsOptions{ResolveOptions: resolve, Settings: changes, Restart: restart})
		if jsonOut {
			if err != nil {
				return err
			}
			return writeJSON(cfg.Stdout, result)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(cfg.Stderr, "warning: %s\n", warning)
		}
		if err != nil {
			return err
		}
		printSettingsResult(cfg, result)
		return nil
	default:
		return usageErr{fmt.Errorf("unknown agent settings action %q", action)}
	}
}

func runAgentProfile(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	usage := usageErr{errors.New("usage: wi agent profile set [--item <selector>] PROFILE [NAME=VALUE...]")}
	if len(args) == 0 || args[0] != "set" {
		return usage
	}
	fs := newFlagSet("agent profile set", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args[1:]); err != nil {
		return usageErr{err}
	}
	if fs.NArg() == 0 {
		return usage
	}
	changes := map[string]string{}
	for _, arg := range fs.Args()[1:] {
		name, value, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return usageErr{fmt.Errorf("setting %q must be NAME=VALUE; an empty VALUE resets it to the profile", arg)}
		}
		changes[name] = value
	}
	m, err := cfg.App.SetAgentProfile(ctx, app.ResolveOptions{Selector: item, CWD: cfg.CWD, Env: cfg.Env}, fs.Arg(0), changes)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, map[string]any{"work_item_id": m.ID, "agent": m.Agent, "agent_settings": m.AgentSettings})
	}
	fmt.Fprintf(cfg.Stdout, "%s: agent profile %s\n", m.ID, m.Agent)
	return nil
}

func printAgentSettings(cfg Config, view app.AgentSettingsView) {
	fmt.Fprintf(cfg.Stdout, "agent: %s (%s)\n", view.Agent, view.Driver)
	for _, key := range sortedSettingKeys(view.Values) {
		origin := "profile"
		if _, ok := view.Overrides[key]; ok {
			origin = "item"
		}
		fmt.Fprintf(cfg.Stdout, "%s: %s (%s)\n", key, view.Values[key], origin)
	}
	if view.RuntimeOnline {
		live := "none"
		if len(view.Live) > 0 {
			live = strings.Join(view.Live, ", ")
		}
		fmt.Fprintf(cfg.Stdout, "runtime: %s, changes live: %s\n", view.RuntimeMode, live)
		for _, key := range sortedSettingKeys(view.Running) {
			fmt.Fprintf(cfg.Stdout, "running %s: %s\n", key, view.Running[key])
		}
	}
	if view.Catalog != nil {
		if view.Values[driverprotocol.SettingModel] == "" && view.Catalog.Default != "" {
			fmt.Fprintf(cfg.Stdout, "default model: %s\n", view.Catalog.Default)
		}
		if view.Values[driverprotocol.SettingReasoning] == "" && view.Catalog.DefaultReasoning != "" {
			fmt.Fprintf(cfg.Stdout, "default reasoning: %s\n", view.Catalog.DefaultReasoning)
		}
		fmt.Fprintln(cfg.Stdout, "models:")
		for _, model := range view.Catalog.Models {
			fmt.Fprintf(cfg.Stdout, "  %s", model.Value)
			if model.Label != "" && model.Label != model.Value {
				fmt.Fprintf(cfg.Stdout, "  %s", model.Label)
			}
			fmt.Fprintln(cfg.Stdout)
		}
		if len(view.Catalog.ReasoningLevels) > 0 {
			fmt.Fprintf(cfg.Stdout, "reasoning levels: %s\n", strings.Join(view.Catalog.ReasoningLevels, ", "))
		}
	} else if view.CatalogError != "" {
		fmt.Fprintf(cfg.Stderr, "warning: model list unavailable: %s\n", view.CatalogError)
	}
}

func sortedSettingKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func printSettingsResult(cfg Config, result app.SetAgentSettingsResult) {
	if len(result.Changed) == 0 {
		fmt.Fprintln(cfg.Stdout, "settings unchanged")
		return
	}
	fmt.Fprintf(cfg.Stdout, "saved: %s\n", strings.Join(result.Changed, ", "))
	if result.Restarted {
		fmt.Fprintln(cfg.Stdout, "restarted the agent on the same conversation to apply them")
	} else if len(result.Applied) > 0 {
		fmt.Fprintf(cfg.Stdout, "applied to the running agent: %s\n", strings.Join(result.Applied, ", "))
	}
	if len(result.Pending) > 0 {
		fmt.Fprintf(cfg.Stdout, "applies when the agent next starts: %s (use --restart to restart it now)\n", strings.Join(result.Pending, ", "))
	}
}

// coordinatorControlOutcomes reads a driver's verdict on a command from the
// daemon's runtime events.
type coordinatorControlOutcomes struct{ client *coordinator.Client }

func (o coordinatorControlOutcomes) Cursor(ctx context.Context, itemID string) (uint64, error) {
	result, err := o.client.RuntimeEvents(ctx, coordinator.RuntimeEventsRequest{ItemID: itemID, Limit: 1})
	return result.LastSequence, err
}

func (o coordinatorControlOutcomes) Wait(ctx context.Context, itemID string, cursor uint64, commandID string, timeout time.Duration) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		result, err := o.client.RuntimeEvents(ctx, coordinator.RuntimeEventsRequest{ItemID: itemID, AfterSequence: cursor, Limit: 256, WaitMillis: 1000})
		if err != nil {
			if ctx.Err() != nil {
				return false, "", errors.New("no answer in time; the change may still apply")
			}
			return false, "", err
		}
		for _, event := range result.Events {
			var payload struct {
				CommandID  string `json:"command_id"`
				ReasonCode string `json:"reason_code"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil || payload.CommandID != commandID {
				continue
			}
			switch event.Type {
			case agent.EventCommandAccepted:
				return true, "", nil
			case agent.EventCommandRejected:
				return false, "the agent rejected the change", nil
			}
		}
		cursor = result.LastSequence
	}
}

// agentSettingFlags collects the --model and --reasoning choices given.
func agentSettingFlags(modelName, reasoning string) map[string]string {
	settings := map[string]string{}
	if modelName = strings.TrimSpace(modelName); modelName != "" {
		settings[driverprotocol.SettingModel] = modelName
	}
	if reasoning = strings.TrimSpace(reasoning); reasoning != "" {
		settings[driverprotocol.SettingReasoning] = reasoning
	}
	if len(settings) == 0 {
		return nil
	}
	return settings
}
