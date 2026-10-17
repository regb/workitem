package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exercisePluginCatalogE2E(t *testing.T, binary, repository string, env []string, configDir, base string) {
	t.Helper()
	runWiE2E(t, binary, repository, env, "daemon", "stop")
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	script := filepath.Join(root, "plugin")
	manifest := filepath.Join(root, "plugin.json")
	asset := filepath.Join(root, "instructions.md")
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf ran > '"+marker+"'\nprintf '%s\\n' \"$WI_PLUGIN_CONFIG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("Static plugin instructions."), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "name": "demo", "command": []string{script}, "commands": []string{"greet"}, "instructions": "instructions.md", "options": []map[string]any{{"name": "message", "type": "string", "default": "default", "repository": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(manifest)
	if err := os.WriteFile(configPath, []byte(base+"[plugins.demo]\nenabled = true\nmanifest = "+string(quoted)+"\n[extensions.demo]\nmessage = \"global\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.WriteFile(configPath, []byte(base), 0600) }()
	schemas := runWiE2E(t, binary, repository, env, "config", "extensions", "--json")
	if !strings.Contains(string(schemas), `"demo"`) || !strings.Contains(string(schemas), `"direnv"`) {
		t.Fatal("common catalog omitted native or external schemas")
	}
	text := runWiE2E(t, binary, repository, env, "plugin", "skill", "demo")
	if strings.TrimSpace(string(text)) != "Static plugin instructions." {
		t.Fatal("static asset lookup failed")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inspection executed plugin code", err)
	}
	var item struct {
		ID string `json:"work_item_id"`
	}
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "new", "--repo", "test", "--title", "Plugin fixture", "--json"), &item)
	runWiE2E(t, binary, repository, env, "repo", "settings", "set", "test", "extensions.demo.message", "repository", "--json")
	var settings map[string]any
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "demo", "greet", "--item", item.ID), &settings)
	if settings["message"] != "repository" {
		t.Fatal("executable command did not receive daemon-owned repository policy")
	}
	runWiE2E(t, binary, repository, env, "repo", "settings", "unset", "test", "extensions.demo.message", "--json")
	runWiE2E(t, binary, repository, env, "state", "set", "closed", "--item", item.ID)
	runWiE2E(t, binary, repository, env, "daemon", "stop")
	if err := os.WriteFile(configPath, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
}
