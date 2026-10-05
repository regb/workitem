package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/agentdriver/drivertest"
	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestConversationOperationRejectsUnknownConfiguration(t *testing.T) {
	request := driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: "pi", Operation: driverprotocol.OperationConversationCreate, Config: []byte(`{"unknown":true}`), Conversation: &driverprotocol.ConversationRequest{ID: "conversation-1"}}
	if _, err := handleConversationOperation(context.Background(), request); err == nil {
		t.Fatal("expected unknown configuration field to fail")
	}
}

func TestConversationSnapshotOperations(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.jsonl")
	content := []byte("{\"type\":\"session\",\"id\":\"snapshot\"}\n")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	exported, err := handleConversationOperation(context.Background(), driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: "pi", Operation: driverprotocol.OperationConversationExport, Conversation: &driverprotocol.ConversationRequest{Path: source}})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.jsonl")
	if _, err := handleConversationOperation(context.Background(), driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: "pi", Operation: driverprotocol.OperationConversationImport, Conversation: &driverprotocol.ConversationRequest{Path: target, Data: exported.Data}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(content) {
		t.Fatalf("content=%q err=%v", got, err)
	}
}

func TestHeadlessRuntimeConformance(t *testing.T) {
	backend := filepath.Join(t.TempDir(), "fake-pi")
	script := `#!/usr/bin/env bash
set -euo pipefail
while IFS= read -r line; do
  case "$line" in
    *wi-runtime-ready*) printf '%s\n' '{"id":"wi-runtime-ready","type":"response","command":"get_state","success":true,"data":{"isStreaming":false}}' ;;
  esac
done
`
	if err := os.WriteFile(backend, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(Config{Executable: backend})
	drivertest.RunRuntime(t, drivertest.RuntimeSuite{Driver: "pi", Mode: driverprotocol.ModeHeadless, Config: config, Launch: launch})
}

func TestDriverConformance(t *testing.T) {
	drivertest.Run(t, drivertest.Suite{Driver: "pi", Exchange: func(ctx context.Context, request driverprotocol.Request) (driverprotocol.Response, error) {
		result, err := handleConversationOperation(ctx, request)
		if err != nil {
			return drivertest.ErrorResponse(err), err
		}
		return driverprotocol.Response{ProtocolVersion: driverprotocol.Version, Conversation: &result}, nil
	}})
}
