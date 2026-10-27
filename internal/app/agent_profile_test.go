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
	a.AgentsConfig["pi-work"] = config.AgentDefinitionConfig{Driver: "pi", Config: map[string]any{"executable": "/bin/pi"}}
	a.AgentsConfig["claude"] = config.AgentDefinitionConfig{Driver: "claude"}
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

	// Another driver cannot resume the conversation.
	if _, err := a.SetAgentProfile(ctx, ResolveOptions{Selector: m.ID}, "claude", nil); err == nil || !strings.Contains(err.Error(), `driver "pi"`) {
		t.Fatalf("other driver err = %v", err)
	}
	moved, err := a.SetAgentProfile(ctx, ResolveOptions{Selector: m.ID}, "pi-work", map[string]string{"reasoning": "high"})
	if err != nil || moved.Agent != "pi-work" || moved.AgentSettings["reasoning"] != "high" {
		t.Fatalf("moved=%+v err=%v", moved, err)
	}
	if err := a.ValidateItemAgentConfig(ctx, ResolveOptions{Selector: m.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetAgentProfile(ctx, ResolveOptions{Selector: m.ID}, "missing", nil); err == nil {
		t.Fatal("moved to an unconfigured profile")
	}
}

// Before the item has a conversation, any profile can take it.
func TestItemWithoutConversationCanMoveToAnyDriver(t *testing.T) {
	a, _, m := settingsApp(t)
	ctx := context.Background()
	a.AgentsConfig["claude"] = config.AgentDefinitionConfig{Driver: "claude"}
	a.DriversConfig["claude"] = config.DriverDefinitionConfig{}
	a.ConversationBackends["claude"] = &settingsBackend{}
	m.PrimaryConversation = nil
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		t.Fatal(err)
	}
	moved, err := a.SetAgentProfile(ctx, ResolveOptions{Selector: m.ID}, "claude", nil)
	if err != nil || moved.Agent != "claude" {
		t.Fatalf("moved=%+v err=%v", moved, err)
	}
}
