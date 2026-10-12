package pi

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/agent"
)

func idleTestTransport(t *testing.T) (*nativeRPCTransport, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "owner", WorkItemID: "item"}, stdin: w, pending: map[string]agent.ControlCommand{}, handled: map[string]bool{}, work: map[string]bool{}, responses: map[string]func(error){}}
	transport.liveDisabled.Store(true)
	return transport, r
}
func idleTestCommand(id, typ string) agent.ControlCommand {
	return agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, RuntimeID: "owner", WorkItemID: "item", ID: id, Type: typ, Message: "work", RequireIdle: true}
}
func TestIdleGuardReservesPromptBeforeAgentStart(t *testing.T) {
	transport, _ := idleTestTransport(t)
	first := idleTestCommand("first", agent.CommandPrompt)
	if err := transport.sendCommand(first); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{agent.CommandPrompt, agent.CommandShutdown} {
		command := idleTestCommand("second", typ)
		if err := transport.sendCommand(command); err == nil {
			t.Fatalf("%s accepted before first agent_start", typ)
		}
	}
	if transport.stopping {
		t.Fatal("refused shutdown changed runtime")
	}
	if err := transport.sendCommand(first); err != nil {
		t.Fatal("duplicate handoff was not idempotent", err)
	}
	if err := transport.handleOutput([]byte(`{"type":"response","id":"first","success":false,"error":"rejected"}`)); err != nil {
		t.Fatal(err)
	}
	if err := transport.sendCommand(idleTestCommand("retry", agent.CommandPrompt)); err != nil {
		t.Fatal("rejection did not release reservation", err)
	}
}
func TestIdlePromptUsesNativeRejectionInsteadOfSteering(t *testing.T) {
	transport, r := idleTestTransport(t)
	command := idleTestCommand("first", agent.CommandPrompt)
	command.Images = []agent.ImageContent{{MIMEType: "image/png", Data: "aW1hZ2U="}}
	if err := transport.sendCommand(command); err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	if err := json.NewDecoder(r).Decode(&native); err != nil {
		t.Fatal(err)
	}
	images, _ := native["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("native command %+v", native)
	}
	image, _ := images[0].(map[string]any)
	if native["type"] != "prompt" || native["message"] != "work" || native["streamingBehavior"] != nil || image["mimeType"] != "image/png" {
		t.Fatalf("native command %+v", native)
	}
}
func TestIdleGuardReleasesHandledAndSettledWork(t *testing.T) {
	for _, records := range [][]string{
		{`{"type":"response","id":"first","success":true,"data":{"disposition":"handled"}}`},
		{`{"type":"response","id":"first","success":true,"data":{"disposition":"started"}}`, `{"type":"agent_start"}`, `{"type":"agent_settled"}`},
		{`{"type":"agent_start"}`, `{"type":"agent_settled"}`, `{"type":"response","id":"first","success":true,"data":{"disposition":"started"}}`},
	} {
		transport, _ := idleTestTransport(t)
		if err := transport.sendCommand(idleTestCommand("first", agent.CommandPrompt)); err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if err := transport.handleOutput([]byte(record)); err != nil {
				t.Fatal(err)
			}
		}
		if err := transport.sendCommand(idleTestCommand("next", agent.CommandPrompt)); err != nil {
			t.Fatalf("reservation retained after %v: %v", records, err)
		}
	}
}
func TestIdleShutdownWaitsForNativeGuardAndDoesNotCloseInput(t *testing.T) {
	transport, r := idleTestTransport(t)
	command := idleTestCommand("shutdown", agent.CommandShutdown)
	command.Message = ""
	if err := transport.sendCommand(command); err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	if err := json.NewDecoder(r).Decode(&native); err != nil {
		t.Fatal(err)
	}
	if native["type"] != "prompt" || !strings.HasPrefix(native["message"].(string), "/wi-control-idle ") {
		t.Fatalf("shutdown bypassed native guard %+v", native)
	}
	seen := false
	transport.responses[command.ID] = func(err error) {
		seen = true
		if err == nil {
			t.Error("native refusal reported as success")
		}
	}
	if err := transport.handleOutput([]byte(`{"type":"response","id":"shutdown","success":true,"data":{"disposition":"handled"}}`)); err != nil {
		t.Fatal(err)
	}
	if seen || transport.stopping {
		t.Fatal("native slash handling bypassed the private guard result")
	}
	if err := transport.handleOutput([]byte(`{"type":"wi_control_result","id":"shutdown","runtime_id":"owner","work_item_id":"item","success":false,"error":"agent became busy"}`)); err != nil {
		t.Fatal(err)
	}
	if !seen || transport.stopping || transport.shutdownPending {
		t.Fatal("refusal lost or runtime changed")
	}
	if err := transport.writeNativeCommand(map[string]any{"type": "get_state"}); err != nil {
		t.Fatal("native input was closed", err)
	}
}

type failingInput struct{}

func (failingInput) Write([]byte) (int, error) { return 0, errors.New("injected write failure") }
func (failingInput) Close() error              { return nil }
func TestFailedHandoffDoesNotReserveWorkOrDeduplicateRetry(t *testing.T) {
	transport, _ := idleTestTransport(t)
	transport.stdin = failingInput{}
	command := idleTestCommand("first", agent.CommandPrompt)
	if err := transport.sendCommand(command); err == nil {
		t.Fatal("write failure ignored")
	}
	if len(transport.work) != 0 || transport.handled[command.ID] {
		t.Fatal("failed handoff changed idle state")
	}
}
