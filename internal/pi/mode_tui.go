package pi

import "context"

type tuiRuntimeMode struct{}

func (tuiRuntimeMode) Args(sessionPath, bridgePath string) ([]string, error) {
	args, err := commonArgs(sessionPath, bridgePath)
	if err != nil {
		return nil, err
	}
	return append(args, "--session", sessionPath), nil
}

func (tuiRuntimeMode) Run(ctx context.Context, runner Runner, path string, args []string, spec ExecSpec, env []string) error {
	return runner.RunInteractive(ctx, path, args, spec.CWD, env)
}
