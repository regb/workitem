package app

import (
	"context"
	"encoding/json"
	"github.com/regb/workitem/internal/config"
	"testing"
)

type titleBackend struct {
	ConversationBackend
	config json.RawMessage
	prompt string
}

func (b *titleBackend) GenerateTitle(_ context.Context, prompt, agent string, config json.RawMessage) (string, error) {
	b.prompt = prompt
	b.config = config
	return "Generated conversation title", nil
}
func TestTitleUsesDefaultAgentDriverConfigWithoutAcquiringConversation(t *testing.T) {
	backend := &titleBackend{}
	a := &App{DefaultAgent: "pi", AgentsConfig: map[string]config.AgentDefinitionConfig{"pi": {Driver: "pi", Config: map[string]any{"executable": "override"}}}, DriversConfig: map[string]config.DriverDefinitionConfig{"pi": {Defaults: map[string]any{"executable": "default", "extensions": []string{"configured"}}}}, ConversationBackends: map[string]ConversationBackend{"pi": backend}}
	title, err := a.GenerateConversationTitle(context.Background(), "task")
	if err != nil || title != "Generated conversation title" || backend.prompt != "task" {
		t.Fatalf("title=%q err=%v", title, err)
	}
	var options map[string]any
	json.Unmarshal(backend.config, &options)
	if options["executable"] != "override" || options["extensions"] == nil {
		t.Fatalf("driver config=%s", backend.config)
	}
	a.ConversationBackends = nil
	if _, err := a.GenerateConversationTitle(context.Background(), "task"); err == nil {
		t.Fatal("missing optional title backend accepted")
	}
}
