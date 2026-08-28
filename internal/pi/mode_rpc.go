package pi

import (
	"context"
	"fmt"
)

type rpcRuntimeMode struct{}

func (rpcRuntimeMode) Args(sessionPath, bridgePath string) ([]string, error) {
	args, err := commonArgs(sessionPath, "")
	if err != nil {
		return nil, err
	}
	return append(args, "--mode", "rpc", "--session", sessionPath), nil
}

func (rpcRuntimeMode) Run(ctx context.Context, runner Runner, path string, args []string, spec ExecSpec, env []string) error {
	headless, ok := runner.(HeadlessRunner)
	if !ok {
		return fmt.Errorf("runner does not support Pi RPC mode")
	}
	return headless.RunHeadless(ctx, path, args, env, spec)
}
