package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/agentdriver/drivertest"
	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/skills"
)

func TestConversationOperationRejectsUnknownConfiguration(t *testing.T) {
	request := driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: "pi", Operation: driverprotocol.OperationConversationCreate, Config: []byte(`{"unknown":true}`), Conversation: &driverprotocol.ConversationRequest{ID: "conversation-1"}}
	if _, err := handleConversationOperation(context.Background(), request); err == nil {
		t.Fatal("expected unknown configuration field to fail")
	}
}

func TestHeadlessRuntimeConformance(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", profile)
	backend := filepath.Join(t.TempDir(), "fake-pi")
	script := `#!/usr/bin/env bash
set -euo pipefail
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--skill" ]; then
    shift
    cat "$1/SKILL.md" > "$SKILL_COPY"
    printf "%s" "$1" > "$SKILL_COPY.path"
  fi
  shift
done
while IFS= read -r line; do
  case "$line" in
    *wi-runtime-ready*) printf '%s\n' '{"id":"wi-runtime-ready","type":"response","command":"get_commands","success":true,"data":{"commands":[{"name":"wi-control-idle"}]}}' ;;
  esac
done
`
	if err := os.WriteFile(backend, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "skill-copy")
	t.Setenv("SKILL_COPY", capture)
	config, _ := json.Marshal(Config{Executable: backend})
	drivertest.RunRuntime(t, drivertest.RuntimeSuite{Driver: "pi", Mode: driverprotocol.ModeHeadless, Config: config, Skills: skills.Bundled(), Launch: launch})
	installed, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(skills.Bundled()[0].Files[0].Data) {
		t.Fatal("headless backend did not receive embedded skill")
	}
	path, err := os.ReadFile(capture + ".path")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(string(path)) {
		t.Fatalf("skill path=%q", path)
	}
	if _, err := os.Stat(filepath.Dir(string(path))); !os.IsNotExist(err) {
		t.Fatalf("temporary skill survived shutdown: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profile, "skills")); !os.IsNotExist(err) {
		t.Fatalf("global profile was modified: %v", err)
	}
}

func TestDriverConformance(t *testing.T) {
	drivertest.Run(t, drivertest.Suite{Driver: "pi", Exchange: func(ctx context.Context, request driverprotocol.Request) (driverprotocol.Response, error) {
		if request.Operation == driverprotocol.OperationConfigValidate {
			if _, err := parseConfig(request.Config); err != nil {
				return drivertest.ErrorResponse(err), err
			}
			return driverprotocol.Response{ProtocolVersion: driverprotocol.Version, ConfigValidation: &driverprotocol.ConfigValidationResult{Valid: true}}, nil
		}
		result, err := handleConversationOperation(ctx, request)
		if err != nil {
			return drivertest.ErrorResponse(err), err
		}
		return driverprotocol.Response{ProtocolVersion: driverprotocol.Version, Conversation: &result}, nil
	}})
}
