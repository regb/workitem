package daemonapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

type discoveryFixture struct {
	profiles []protocol.DiscoveredAgentConfig
	err      error
}

func (f discoveryFixture) DiscoverConfig(context.Context) (protocol.ConfigDiscoveryResult, error) {
	return protocol.ConfigDiscoveryResult{Profiles: f.profiles}, f.err
}
func (discoveryFixture) ValidateConfig(context.Context, string, json.RawMessage) error { return nil }

type profileStore struct{ profiles model.AgentProfiles }

func (s *profileStore) AgentProfiles(context.Context) (model.AgentProfiles, error) {
	return s.profiles, nil
}
func (s *profileStore) ChangeAgentProfile(_ context.Context, command coordinator.ChangeAgentProfileCommand) (model.AgentProfiles, error) {
	s.profiles.Profiles = append(s.profiles.Profiles, *command.Put)
	if s.profiles.Default == "" {
		s.profiles.Default = command.Put.Name
	}
	return s.profiles, nil
}

func TestSeedingAddsDiscoveredAgentsOnceWithPiFirst(t *testing.T) {
	store := &profileStore{}
	clients := map[string]configDiscoverer{
		"claude": discoveryFixture{profiles: []protocol.DiscoveredAgentConfig{{Name: "claude", Label: "Claude Code", Config: json.RawMessage(`{"executable":"/bin/claude"}`)}}},
		"pi":     discoveryFixture{profiles: []protocol.DiscoveredAgentConfig{{Name: "pi", Label: "Pi", Config: json.RawMessage(`{}`)}}},
		"other":  discoveryFixture{err: errors.New("not installed")},
	}
	var out bytes.Buffer
	if err := seedAgentProfiles(context.Background(), store, clients, &out); err != nil {
		t.Fatal(err)
	}
	if store.profiles.Default != "pi" || len(store.profiles.Profiles) != 2 || !strings.Contains(out.String(), "Added 2 agent profiles") || strings.Contains(out.String(), "not installed") {
		t.Fatalf("profiles=%+v out=%s", store.profiles, &out)
	}
	if err := seedAgentProfiles(context.Background(), store, clients, &out); err != nil || len(store.profiles.Profiles) != 2 {
		t.Fatal("seeded again", store.profiles, err)
	}
}
