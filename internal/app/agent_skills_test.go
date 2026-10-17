package app

import (
	"bytes"
	"context"
	"github.com/regb/workitem/internal/plugin"
	"testing"
)

func TestAgentSkillsIncludeOnlyEnabledStaticInstructions(t *testing.T) {
	registry, err := plugin.NewRegistry(plugin.Definition{Name: "github", Command: []string{"does-not-exist"}, Instructions: "Wait on pull requests."}, plugin.Definition{Name: "jira", Command: []string{"does-not-exist"}, Instructions: "Jira text."})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Plugins: plugin.NewHost(registry, map[string]plugin.Registration{"github": {Enabled: true}}, nil, "wi", nil)}
	var content []byte
	for _, file := range a.agentSkills(context.Background(), "")[0].Files {
		if file.Path == "SKILL.md" {
			content = file.Data
		}
	}
	if !bytes.Contains(content, []byte("### Plugin: github\n\nWait on pull requests.")) || bytes.Contains(content, []byte("Jira text.")) {
		t.Fatal("static enabled instruction selection failed")
	}
}
