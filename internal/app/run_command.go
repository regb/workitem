package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/regb/workitem/internal/agent"
)

// RunCommand runs shell code in the item's checkout as a terminal the
// person opened there would: in the environment its agents get, the
// repository's variables and its loaded .envrc, but with no agent
// credential, so wi commands it runs act as the person. Its output and
// errors go to out. It returns the command's exit status, -1 when a signal
// ended it, as when ctx ended.
func (a *App) RunCommand(ctx context.Context, opts ResolveOptions, command string, out io.Writer) (int, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return 0, err
	}
	claim := m.Checkout.Claim
	if claim == nil {
		return 0, fmt.Errorf("item %s has no checkout to run a command in", m.ID)
	}
	env, _, warnings, err := a.agentRuntimeEnvironment(ctx, m, agent.ModeHeadless)
	if err != nil {
		return 0, err
	}
	for _, warning := range warnings {
		fmt.Fprintf(out, "warning: %s\n", warning)
	}
	env["WI_ID"], env["WI_WORKTREE"] = m.ID, claim.Path
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = claim.Path
	cmd.Env = exactRuntimeEnvironment(env)
	cmd.Stdout, cmd.Stderr = out, out
	// The command may start processes of its own: stopping it stops them
	// all.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		return exit.ExitCode(), nil
	case ctx.Err() != nil:
		return -1, nil
	default:
		return 0, err
	}
}
