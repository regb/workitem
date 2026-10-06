package executor

import (
	"context"
	"fmt"
	"io"

	executorprotocol "github.com/regb/workitem/executor/protocol"
)

func Serve(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	request, err := executorprotocol.ReadRequest(stdin)
	if err != nil {
		_ = executorprotocol.WriteResponse(stdout, executorprotocol.Response{Error: err.Error()})
		return nil
	}
	response, handleErr := (Worker{}).Handle(ctx, request)
	if handleErr != nil {
		response = executorprotocol.Response{Error: handleErr.Error()}
	}
	if err := executorprotocol.WriteResponse(stdout, response); err != nil {
		return fmt.Errorf("write executor response: %w", err)
	}
	return nil
}
