package config_test

import (
	"github.com/regb/workitem/internal/config"
	"path/filepath"
	"strconv"
	"testing"
)

func TestAppendSystemPromptConfiguration(t *testing.T) {
	if got := config.Default().Agent.AppendSystemPrompt; got != config.DefaultAppendSystemPrompt || got == "" {
		t.Fatalf("default=%q", got)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, prompt := range []string{"Custom instructions.\nPreserve formatting.", "", "   "} {
		writeFile(t, path, "[agent]\nappend_system_prompt = "+strconv.Quote(prompt)+"\n")
		cfg, warnings, err := config.Load(path)
		if err != nil || len(warnings) != 0 || cfg.Agent.AppendSystemPrompt != prompt || cfg.Agent.StartupMessage != config.DefaultStartupMessage {
			t.Fatalf("cfg=%+v warnings=%v error=%v", cfg.Agent, warnings, err)
		}
	}
	for _, value := range []string{"42", `["prompt"]`, `"unterminated`, strconv.Quote("bad\x00prompt")} {
		writeFile(t, path, "[agent]\nappend_system_prompt = "+value+"\n")
		if _, _, err := config.Load(path); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}

func TestStartupMessageConfiguration(t *testing.T) {
	defaults := config.Default()
	if defaults.Agent.StartupMessage != config.DefaultStartupMessage {
		t.Fatalf("defaults=%+v", defaults.Agent)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[agent]\nstartup_message = \"Read wi show.\\nCheck acceptance criteria.\"\n")
	cfg, warnings, err := config.Load(path)
	if err != nil || len(warnings) != 0 || cfg.Agent.StartupMessage != "Read wi show.\nCheck acceptance criteria." {
		t.Fatalf("agent=%+v warnings=%v error=%v", cfg.Agent, warnings, err)
	}
	for _, value := range []string{`""`, `"   "`, `42`, `["message"]`, `"unterminated`} {
		writeFile(t, path, "[agent]\nstartup_message = "+value+"\n")
		if _, _, err := config.Load(path); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
