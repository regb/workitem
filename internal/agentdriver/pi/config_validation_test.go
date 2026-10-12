package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/agentdriver"
)

func TestConfigValidationIsStatelessAndSharesLaunchRules(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", profile)
	marker := filepath.Join(t.TempDir(), "backend-started")
	executable := filepath.Join(t.TempDir(), "fake-pi")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client := agentdriver.Client{Driver: "pi", Path: os.Args[0], Args: []string{"-test.run=^TestDecodeTransportHelper$"}, Env: append(os.Environ(), "GO_WANT_WI_PI_DECODE_HELPER=1")}
	for _, tc := range []struct {
		name  string
		raw   json.RawMessage
		valid bool
	}{
		{"defaults", json.RawMessage(`{}`), true},
		{"optional overrides", json.RawMessage(`{"model":"provider/model","thinking":" high "}`), true},
		{"unknown field", json.RawMessage(`{"unknown":true}`), false},
		{"invalid thinking", json.RawMessage(`{"thinking":"extreme"}`), false},
		{"wrong type", json.RawMessage(`{"model":42}`), false},
		{"malformed", json.RawMessage(`{"model":`), false},
		{"trailing object", json.RawMessage(`{} {}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, parseErr := parseConfig(tc.raw)
			if (parseErr == nil) != tc.valid {
				t.Fatalf("parseConfig err=%v", parseErr)
			}
			// Malformed raw JSON cannot be encoded in an envelope; direct parsing above
			// covers it. Other failures must cross the real process protocol.
			if !json.Valid(tc.raw) {
				return
			}
			err := client.ValidateConfig(context.Background(), "default", tc.raw)
			if (err == nil) != tc.valid {
				t.Fatalf("config.validate err=%v", err)
			}
		})
	}
	raw, _ := json.Marshal(Config{Executable: executable, Model: "provider/model", Thinking: "high"})
	if err := client.ValidateConfig(context.Background(), "default", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("validation launched the backend")
	}
	entries, err := os.ReadDir(profile)
	if err != nil || len(entries) != 0 {
		t.Fatalf("validation changed profile: %v %v", entries, err)
	}
}
