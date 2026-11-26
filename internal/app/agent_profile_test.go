package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
)

func TestRemovedProfileExplainsHowToMoveTheItem(t *testing.T) {
	a, _, m := settingsApp(t)
	ctx := context.Background()
	a.StaticAgentProfiles.Profiles = append(a.StaticAgentProfiles.Profiles, model.AgentProfile{Name: "pi-work", Driver: "pi", Config: map[string]any{"executable": "/bin/pi"}})
	a.StaticAgentProfiles.Profiles = append(a.StaticAgentProfiles.Profiles, model.AgentProfile{Name: "claude", Driver: "claude"})
	a.DriversConfig["claude"] = config.DriverDefinitionConfig{}
	m.Agent = "pi-old"
	m.PrimaryConversation = &model.Conversation{Driver: "pi", ID: "conversation"}
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		t.Fatal(err)
	}
	err := a.ValidateItemAgentConfig(ctx, ResolveOptions{Selector: m.ID})
	var missing *MissingAgentProfileError
	if !errors.As(err, &missing) || missing.Profile != "pi-old" || strings.Join(missing.Choices, ",") != "pi,pi-work" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "wi agent profile set") {
		t.Fatalf("message gives no remedy: %v", err)
	}
	if _, err := a.AgentSettings(ctx, ResolveOptions{Selector: m.ID}); !errors.As(err, &missing) {
		t.Fatalf("settings err = %v", err)
	}
}
