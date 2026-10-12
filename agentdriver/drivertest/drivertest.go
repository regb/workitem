// Package drivertest provides reusable contract checks for agent drivers.
package drivertest

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
	TempDir() string
}

type Exchange func(context.Context, driverprotocol.Request) (driverprotocol.Response, error)

type Suite struct {
	Driver   string
	Config   json.RawMessage
	Exchange Exchange
}

// Run checks config validation, protocol identity, conversation creation, and rejection of an
// unknown operation. Driver packages can add backend-specific cases beside it.
func Run(t TestingT, suite Suite) {
	t.Helper()
	if strings.TrimSpace(suite.Driver) == "" || suite.Exchange == nil {
		t.Fatalf("driver name and exchange function are required")
	}
	validated, err := suite.Exchange(context.Background(), driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: suite.Driver, Operation: driverprotocol.OperationConfigValidate, Config: suite.Config})
	if err != nil || validated.ProtocolVersion != driverprotocol.Version || validated.ConfigValidation == nil || !validated.ConfigValidation.Valid {
		t.Fatalf("invalid config.validate response: %+v err=%v", validated, err)
	}
	create := driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: suite.Driver, Config: suite.Config, Operation: driverprotocol.OperationConversationCreate, Conversation: &driverprotocol.ConversationRequest{ID: "01TESTDRIVERCONVERSATION0000"}}
	response, err := suite.Exchange(context.Background(), create)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if response.ProtocolVersion != driverprotocol.Version || response.Conversation == nil {
		t.Fatalf("invalid create response: %+v", response)
	}
	conversation := response.Conversation.Conversation
	if conversation.ID != create.Conversation.ID || conversation.Locator == "" || conversation.Format == "" {
		t.Fatalf("invalid conversation descriptor: %+v", conversation)
	}
	unknown := driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: suite.Driver, Operation: "conformance.unknown", Conversation: &driverprotocol.ConversationRequest{}}
	response, err = suite.Exchange(context.Background(), unknown)
	if err == nil && response.Error == "" {
		t.Fatalf("driver accepted unknown operation: %+v", response)
	}
}

type RuntimeSuite struct {
	Driver string
	Mode   driverprotocol.Mode
	Config json.RawMessage
	Skills []driverprotocol.Skill
	Launch func(context.Context, driverprotocol.LaunchRequest, func(driverprotocol.Ready) error) error
}

// RunRuntime checks launch negotiation and the public control and event
// transports against a backend fixture supplied by the driver package.
func RunRuntime(t TestingT, suite RuntimeSuite) {
	t.Helper()
	if strings.TrimSpace(suite.Driver) == "" || suite.Launch == nil {
		t.Fatalf("driver name and launch function are required")
	}
	dir := t.TempDir()
	controlPath, eventPath := filepath.Join(dir, "control.sock"), filepath.Join(dir, "events.sock")
	events, err := driverprotocol.ListenEvents(eventPath)
	if err != nil {
		t.Fatalf("listen for events: %v", err)
	}
	defer events.Close()
	eventTypes := make(chan string, 32)
	go func() {
		for request := range events.Requests() {
			request.Respond(nil)
			select {
			case eventTypes <- request.Event.Type:
			default:
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan driverprotocol.Ready, 1)
	done := make(chan error, 1)
	request := driverprotocol.LaunchRequest{ProtocolVersion: driverprotocol.Version, Driver: suite.Driver, Agent: "default", Config: suite.Config, Skills: suite.Skills, Runtime: driverprotocol.Runtime{ID: "runtime-conformance", WorkItemID: "01KZYHGDCECSFS4BJ2SNTQP49V", Mode: suite.Mode, CWD: dir, ControlSocketPath: controlPath, EventSocketPath: eventPath}, Conversation: driverprotocol.Conversation{ID: "conversation-conformance", Locator: filepath.Join(dir, "conversation.jsonl"), Format: "test"}}
	go func() {
		done <- suite.Launch(ctx, request, func(value driverprotocol.Ready) error { ready <- value; return nil })
	}()
	var negotiated driverprotocol.Ready
	select {
	case negotiated = <-ready:
	case err := <-done:
		t.Fatalf("driver exited before ready: %v", err)
	case <-ctx.Done():
		t.Fatalf("driver readiness: %v", ctx.Err())
	}
	if len(suite.Skills) > 0 && !negotiated.Capabilities.BundledSkills {
		t.Fatalf("driver does not support bundled skills: %+v", negotiated)
	}
	if suite.Mode == driverprotocol.ModeHeadless && !negotiated.Capabilities.Headless {
		t.Fatalf("driver did not advertise headless support: %+v", negotiated.Capabilities)
	}
	if suite.Mode == driverprotocol.ModeTUI && !negotiated.Capabilities.NativeTUI {
		t.Fatalf("driver did not advertise TUI support: %+v", negotiated.Capabilities)
	}
	for {
		select {
		case eventType := <-eventTypes:
			if eventType == driverprotocol.EventRuntimeReady {
				goto readyEvent
			}
		case err := <-done:
			t.Fatalf("driver exited before runtime.ready: %v", err)
		case <-ctx.Done():
			t.Fatalf("runtime.ready: %v", ctx.Err())
		}
	}
readyEvent:
	shutdown := driverprotocol.ControlCommand{ProtocolVersion: driverprotocol.RuntimeVersion, ID: "shutdown-conformance", RuntimeID: request.Runtime.ID, WorkItemID: request.Runtime.WorkItemID, Type: driverprotocol.CommandShutdown, CreatedAt: time.Now()}
	for {
		err = driverprotocol.SubmitControl(ctx, controlPath, shutdown)
		if err == nil {
			break
		}
		if _, rejected := err.(*driverprotocol.ControlRejectedError); rejected {
			t.Fatalf("shutdown rejected: %v", err)
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("submit shutdown: %v", ctx.Err())
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("driver shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("driver shutdown: %v", ctx.Err())
	}
}

func ErrorResponse(err error) driverprotocol.Response {
	return driverprotocol.Response{ProtocolVersion: driverprotocol.Version, Error: fmt.Sprint(err)}
}
