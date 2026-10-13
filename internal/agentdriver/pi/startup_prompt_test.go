package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/testutil"
)

func TestStartupPromptIsNotAcceptedUntilNativeReadiness(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	backend := filepath.Join(dir, "backend")
	marker := filepath.Join(dir, "ready")
	capture := filepath.Join(dir, "prompt")
	script := `#!/usr/bin/env bash
set -euo pipefail
IFS= read -r probe
while [ ! -f "$1" ]; do sleep 0.01; done
printf '%s\n' '{"type":"response","id":"wi-runtime-ready","command":"get_commands","success":true,"data":{"commands":[{"name":"wi-control-idle"}]}}'
IFS= read -r prompt
printf '%s' "$prompt" > "$2"
printf '%s\n' '{"type":"response","id":"initial-prompt","command":"prompt","success":true}'
while IFS= read -r ignored; do :; done
`
	if err := os.WriteFile(backend, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := driverprotocol.ListenEvents(filepath.Join(dir, "events.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	go func() {
		for request := range events.Requests() {
			request.Respond(nil)
		}
	}()
	socket := filepath.Join(dir, "control.sock")
	done := make(chan error, 1)
	go func() {
		done <- runNativeRPC(ctx, backend, []string{marker, capture}, os.Environ(), ExecSpec{CWD: dir, RuntimeID: "owner", WorkItemID: "item", ControlSocketPath: socket, EventSocketPath: filepath.Join(dir, "events.sock")})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("runtime exited before control startup: %v", err)
		default:
		}
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket did not appear")
		}
		time.Sleep(time.Millisecond)
	}
	requestCtx, requestCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer requestCancel()
	submitted := make(chan error, 1)
	go func() {
		submitted <- agent.SubmitControlSocket(requestCtx, socket, agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "initial-prompt", RuntimeID: "owner", WorkItemID: "item", Type: agent.CommandPrompt, Message: "Fix the proposal links"})
	}()
	select {
	case err := <-submitted:
		t.Fatalf("prompt completed before native readiness: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-requestCtx.Done():
		t.Fatal("initial prompt was lost after readiness")
	}
	// Control acceptance acknowledges the handoff, not completion of the
	// backend's asynchronous read and capture write.
	for {
		data, err := os.ReadFile(capture)
		if err == nil && strings.Contains(string(data), "Fix the proposal links") {
			break
		}
		if requestCtx.Err() != nil {
			t.Fatalf("native prompt=%s err=%v", data, err)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-requestCtx.Done():
		t.Fatal("runtime did not stop")
	}
}
