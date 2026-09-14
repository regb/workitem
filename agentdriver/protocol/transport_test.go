package driverprotocol

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapHandshake(t *testing.T) {
	server, err := Listen(filepath.Join(t.TempDir(), "bootstrap.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := LaunchRequest{ProtocolVersion: Version, Driver: "test", Agent: "default", Runtime: Runtime{ID: "runtime-1"}}
	result := make(chan error, 1)
	go func() {
		got, reply, err := Connect(ctx, server.path, Hello{ProtocolVersion: Version, Driver: "test"})
		if err == nil && (got.Driver != "test" || got.Runtime.ID != "runtime-1") {
			t.Errorf("request=%+v", got)
		}
		if err == nil {
			err = reply(Ready{ProtocolVersion: Version, Capabilities: Capabilities{Prompt: true}})
		}
		result <- err
	}()
	ready, err := server.ServeOnce(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if !ready.Capabilities.Prompt {
		t.Fatalf("ready=%+v", ready)
	}
}

func TestConversationDecodeTransportsMalformedNativeLines(t *testing.T) {
	request := Request{ProtocolVersion: Version, Driver: "test", Operation: OperationConversationDecode, Conversation: &ConversationRequest{Lines: [][]byte{[]byte(`{"type":"message"} trailing`)}}}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Request
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Conversation.Lines) != 1 || string(decoded.Conversation.Lines[0]) != `{"type":"message"} trailing` {
		t.Fatalf("lines=%q", decoded.Conversation.Lines)
	}
}

func TestConversationOperationHandshake(t *testing.T) {
	server, err := Listen(filepath.Join(t.TempDir(), "bootstrap.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := Request{ProtocolVersion: Version, Driver: "test", Operation: OperationConversationCreate, Conversation: &ConversationRequest{ID: "conversation-1"}}
	result := make(chan error, 1)
	go func() {
		got, reply, err := ConnectRequest(ctx, server.path, Hello{ProtocolVersion: Version, Driver: "test"})
		if err == nil && (got.Operation != OperationConversationCreate || got.Conversation == nil || got.Conversation.ID != "conversation-1") {
			t.Errorf("request=%+v", got)
		}
		if err == nil {
			response := ConversationResponse{Conversation: Conversation{ID: "conversation-1", Locator: "sessions/test/conversation-1.jsonl", Format: "test-jsonl"}}
			err = reply(Response{ProtocolVersion: Version, Conversation: &response})
		}
		result <- err
	}()
	response, err := server.ServeRequestOnce(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if response.Conversation == nil || response.Conversation.Conversation.Format != "test-jsonl" {
		t.Fatalf("response=%+v", response)
	}
}
