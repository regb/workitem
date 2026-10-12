package agentdriver

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestRunOperationUsesDriverProcess(t *testing.T) {
	request := driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: "test", Operation: driverprotocol.OperationConversationCreate, Conversation: &driverprotocol.ConversationRequest{ID: "conversation-1"}}
	response, err := RunOperation(context.Background(), os.Args[0], []string{"-test.run=TestOperationHelperProcess"}, request, append(os.Environ(), "GO_WANT_WI_DRIVER_HELPER=1"))
	if err != nil {
		t.Fatal(err)
	}
	if response.Conversation == nil || response.Conversation.Conversation.Locator != "sessions/test/conversation-1.jsonl" {
		t.Fatalf("response=%+v", response)
	}
}

func TestClientConfigValidationIsExplicitAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		legacy       bool
		wantError    string
	}{
		{"valid", `{"required":"present"}`, false, ""},
		{"missing required field", `{}`, false, "required setting is missing"},
		{"legacy driver", `{"required":"present"}`, true, "did not acknowledge config.validate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := append(os.Environ(), "GO_WANT_WI_DRIVER_HELPER=1")
			if tc.legacy {
				env = append(env, "WI_TEST_LEGACY_VALIDATION=1")
			}
			client := Client{Driver: "test", Path: os.Args[0], Args: []string{"-test.run=^TestOperationHelperProcess$"}, Env: env}
			err := client.ValidateConfig(context.Background(), "profile", json.RawMessage(tc.config))
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestConfigValidationHonorsCancellation(t *testing.T) {
	client := Client{Driver: "test", Path: os.Args[0], Args: []string{"-test.run=^TestOperationHelperProcess$"}, Env: append(os.Environ(), "GO_WANT_WI_DRIVER_HELPER=1", "WI_TEST_HANG_VALIDATION=1")}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := client.ValidateConfig(ctx, "profile", json.RawMessage(`{"required":"present"}`)); err == nil {
		t.Fatal("hung validation succeeded")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("validation ignored cancellation")
	}
}

func TestOperationHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_WI_DRIVER_HELPER") != "1" {
		return
	}
	request, reply, err := driverprotocol.ConnectRequest(context.Background(), os.Getenv(driverprotocol.BootstrapSocketEnv), driverprotocol.Hello{ProtocolVersion: driverprotocol.Version, Driver: "test"})
	if err != nil {
		os.Exit(2)
	}
	if request.Operation == driverprotocol.OperationConfigValidate {
		if os.Getenv("WI_TEST_HANG_VALIDATION") == "1" {
			for {
				time.Sleep(time.Second)
			}
		}
		response := driverprotocol.Response{ProtocolVersion: driverprotocol.Version}
		if os.Getenv("WI_TEST_LEGACY_VALIDATION") == "1" {
			response.Conversation = &driverprotocol.ConversationResponse{}
		} else {
			var config map[string]string
			if json.Unmarshal(request.Config, &config) != nil || config["required"] != "present" {
				response.Error = "required setting is missing"
			} else {
				response.ConfigValidation = &driverprotocol.ConfigValidationResult{Valid: true}
			}
		}
		if err := reply(response); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	conversation := driverprotocol.ConversationResponse{Conversation: driverprotocol.Conversation{ID: request.Conversation.ID, Locator: "sessions/test/" + request.Conversation.ID + ".jsonl", Format: "test-jsonl"}}
	if err := reply(driverprotocol.Response{ProtocolVersion: driverprotocol.Version, Conversation: &conversation}); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
