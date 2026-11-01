package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// configDiscoverer finds a driver's installed agents.
type configDiscoverer interface {
	DiscoverConfig(context.Context) (protocol.ConfigDiscoveryResult, error)
	ValidateConfig(context.Context, string, json.RawMessage) error
}

type agentProfileChanger interface {
	AgentProfiles(context.Context) (model.AgentProfiles, error)
	ChangeAgentProfile(context.Context, coordinator.ChangeAgentProfileCommand) (model.AgentProfiles, error)
}

// seedAgentProfiles gives a daemon without agent profiles one for each agent
// its drivers find installed, as the first command run does. Pi's comes
// first, so it is the default. Nothing changes once a profile exists.
func seedAgentProfiles(ctx context.Context, daemon agentProfileChanger, clients map[string]configDiscoverer, warn io.Writer) error {
	current, err := daemon.AgentProfiles(ctx)
	if err != nil || len(current.Profiles) > 0 {
		return err
	}
	drivers := make([]string, 0, len(clients))
	for driver := range clients {
		drivers = append(drivers, driver)
	}
	sort.Slice(drivers, func(i, j int) bool {
		if (drivers[i] == "pi") != (drivers[j] == "pi") {
			return drivers[i] == "pi"
		}
		return drivers[i] < drivers[j]
	})
	added := 0
	for _, driver := range drivers {
		// An agent that is not installed is not news: commands that need a
		// profile say how to add one.
		discovered, err := clients[driver].DiscoverConfig(ctx)
		if err != nil {
			continue
		}
		for _, candidate := range discovered.Profiles {
			values, err := discoveredValues(candidate.Config)
			if err == nil {
				err = clients[driver].ValidateConfig(ctx, candidate.Name, candidate.Config)
			}
			if err == nil {
				_, err = daemon.ChangeAgentProfile(ctx, coordinator.ChangeAgentProfileCommand{Put: &model.AgentProfile{Name: candidate.Name, Driver: driver, Label: candidate.Label, Config: values}})
			}
			if err != nil {
				fmt.Fprintf(warn, "warning: %s discovered profile %s was not added: %v\n", driver, candidate.Name, err)
				continue
			}
			added++
		}
	}
	if added > 0 {
		profiles, _ := daemon.AgentProfiles(ctx)
		fmt.Fprintf(warn, "Added %d agent profiles for the agents found installed; the default is %s. See wi agent profile list.\n", added, profiles.Default)
	}
	return nil
}

var configKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// discoveredValues decodes one discovered configuration object.
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
	for key := range values {
		if !configKey.MatchString(key) {
			return nil, fmt.Errorf("invalid configuration key %q", key)
		}
	}
	return values, nil
}
