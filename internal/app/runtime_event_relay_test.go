package app

import (
	"errors"
	"fmt"
	"io"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/coordinator"
)

func TestRuntimeRelaySeparatesTransportFailureFromOwnerRejection(t *testing.T) {
	if runtimeRelayError(nil) != nil {
		t.Fatal("successful relay became an error")
	}
	for _, err := range []error{io.EOF, fmt.Errorf("dial coordinator: %w", io.ErrClosedPipe), &coordinator.ProtocolError{Code: "database_error", Message: "cannot persist event"}} {
		var rejected *protocol.EventRejectedError
		if !errors.As(runtimeRelayError(err), &rejected) || !rejected.Retryable {
			t.Fatal("temporary daemon failure permanently rejected the producer", err)
		}
	}
	for _, err := range []error{&coordinator.ProtocolError{Code: "runtime_owner_mismatch", Message: "old owner"}, &coordinator.ProtocolError{Code: "runtime_event_error", Message: "invalid event"}, &coordinator.CompatibilityError{Kind: "protocol"}} {
		var rejected *protocol.EventRejectedError
		if errors.As(runtimeRelayError(err), &rejected) && rejected.Retryable {
			t.Fatal("definitive rejection became retryable", err)
		}
	}
}
