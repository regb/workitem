package pi

import (
	"context"
	"fmt"

	"github.com/regb/workitem/internal/agent"
)

type runtimeMode interface {
	Args(sessionPath, bridgePath string) ([]string, error)
	Run(ctx context.Context, runner Runner, path string, args []string, spec ExecSpec, env []string) error
}

func runtimeModeFor(mode agent.Mode) (runtimeMode, error) {
	switch mode {
	case agent.ModeTUI:
		return tuiRuntimeMode{}, nil
	case agent.ModeRPC:
		return rpcRuntimeMode{}, nil
	default:
		return nil, fmt.Errorf("invalid Pi runtime mode %q", mode)
	}
}

func commonArgs(sessionPath, bridgePath string) ([]string, error) {
	if sessionPath == "" {
		return nil, fmt.Errorf("pi session path is required")
	}
	args := []string{}
	if bridgePath != "" {
		args = append(args, "--extension", bridgePath)
	}
	return args, nil
}
