package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutableDiscoveryDoesNotInvokeAgent(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	version := filepath.Join(dir, "agent-version")
	if err := os.WriteFile(version, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "agent")
	if err := os.Symlink(version, launcher); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	result, err := Executable("test-agent", "Test agent", "agent")
	if err != nil || len(result.Profiles) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	var cfg struct {
		Executable string `json:"executable"`
	}
	if err := json.Unmarshal(result.Profiles[0].Config, &cfg); err != nil || cfg.Executable != launcher {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("discovery executed the agent")
	}
	missing, err := Executable("missing", "Missing", "missing")
	if err != nil || len(missing.Profiles) != 0 {
		t.Fatalf("%+v %v", missing, err)
	}
}
