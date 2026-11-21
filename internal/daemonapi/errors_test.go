package daemonapi

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/regb/workitem/internal/apperr"
	"github.com/regb/workitem/internal/coordinator"
)

// An error's kind decides its code, also across the coordinator protocol;
// its wording no longer can make a malformed request look stale.
func TestErrorCodesComeFromKinds(t *testing.T) {
	for _, c := range []struct {
		err  error
		want connect.Code
	}{
		{apperr.Invalidf("workspace claim id and generation must either both be set or both be absent"), connect.CodeInvalidArgument},
		{fmt.Errorf("start: %w", apperr.Stalef("workspace claim generation 3 does not follow 2")), connect.CodeAborted},
		{&coordinator.ProtocolError{Code: "command_error", Message: "the schedule moved on", Kind: "stale"}, connect.CodeAborted},
		{&coordinator.ProtocolError{Code: "command_error", Message: "work item not found", Kind: "not_found"}, connect.CodeNotFound},
		{apperr.Exhaustedf("agent generation exhausted"), connect.CodeResourceExhausted},
		{errors.New("a runtime of another generation is starting"), connect.CodeFailedPrecondition},
		{errors.New("item revision conflict: expected 2, current 3"), connect.CodeAborted},
	} {
		if got := connect.CodeOf(rpcError(c.err)); got != c.want {
			t.Errorf("%q: %v, want %v", c.err, got, c.want)
		}
	}
}
