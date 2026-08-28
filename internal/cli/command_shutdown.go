package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/app"
)

type shutdownCommandResult struct {
	Scheduled      bool                     `json:"scheduled"`
	WorkerPID      int                      `json:"worker_pid,omitempty"`
	LogPath        string                   `json:"log_path,omitempty"`
	Items          []app.ShutdownItemResult `json:"items,omitempty"`
	OrphanedClosed []string                 `json:"orphaned_terminals_closed,omitempty"`
	DaemonStopped  bool                     `json:"daemon_stopped"`
	Failures       []app.ShutdownFailure    `json:"failures"`
}

func runShutdown(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("shutdown", cfg.Stderr)
	var force, worker bool
	fs.BoolVar(&force, "force", false, "abort busy agents and terminate verified runtime process groups when graceful shutdown times out")
	fs.BoolVar(&worker, "worker", false, "internal detached shutdown worker")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 {
		return usageErr{fmt.Errorf("shutdown accepts no positional arguments")}
	}
	if !worker && shutdownNeedsDetachedWorker(cfg.Env) {
		return scheduleShutdownWorker(force, cfg, jsonOut)
	}

	operationEnv := cfg.Env
	if worker {
		// Let the scheduling process flush the worker PID and log path before a
		// forceful shutdown can close its own pane.
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		operationEnv = shutdownOperationEnvironment(cfg.Env)
	}
	shutdown, shutdownErr := cfg.App.ShutdownAll(ctx, operationEnv, force)
	result := shutdownCommandResult{
		Items:          shutdown.Items,
		OrphanedClosed: shutdown.OrphanedTerminalsClosed,
		Failures:       append([]app.ShutdownFailure{}, shutdown.Failures...),
	}
	if shutdownErr != nil && len(result.Failures) == 0 {
		result.Failures = append(result.Failures, app.ShutdownFailure{Resource: "shutdown", Error: shutdownErr.Error()})
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := cfg.Coordinator.Shutdown(requestCtx)
	cancel()
	if err != nil {
		result.Failures = append(result.Failures, app.ShutdownFailure{Resource: "daemon", Error: err.Error()})
	} else {
		result.DaemonStopped = waitForDaemonExit(ctx, cfg, 3*time.Second)
		if !result.DaemonStopped {
			result.Failures = append(result.Failures, app.ShutdownFailure{Resource: "daemon", Error: "daemon did not exit within 3s"})
		}
	}

	if jsonOut {
		if err := writeJSON(cfg.Stdout, result); err != nil {
			return err
		}
	} else {
		printShutdownResult(cfg, result)
	}
	if len(result.Failures) > 0 {
		return fmt.Errorf("shutdown incomplete: %d resource failure(s)", len(result.Failures))
	}
	return nil
}

func shutdownNeedsDetachedWorker(env map[string]string) bool {
	return strings.TrimSpace(env["WI_AGENT_RUNTIME_ID"]) != "" || strings.TrimSpace(env["TMUX"]) != ""
}

func scheduleShutdownWorker(force bool, cfg Config, jsonOut bool) error {
	self := strings.TrimSpace(cfg.App.SelfPath)
	if self == "" {
		resolved, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve wi executable: %w", err)
		}
		self = resolved
	}
	stateRoot := strings.TrimSpace(cfg.StateRoot)
	if stateRoot == "" {
		return fmt.Errorf("shutdown state directory is not configured")
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return fmt.Errorf("create shutdown state directory: %w", err)
	}
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		return fmt.Errorf("secure shutdown state directory: %w", err)
	}
	logPath := filepath.Join(stateRoot, "shutdown.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open shutdown log: %w", err)
	}
	if err := logFile.Chmod(0o600); err != nil {
		logFile.Close()
		return fmt.Errorf("secure shutdown log: %w", err)
	}
	arguments := []string{"shutdown", "--worker"}
	if force {
		arguments = append(arguments, "--force")
	}
	command := exec.Command(self, arguments...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = shutdownWorkerEnvironment(os.Environ())
	command.SysProcAttr = detachedProcessAttributes()
	if err := command.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start detached shutdown worker: %w", err)
	}
	pid := command.Process.Pid
	_ = command.Process.Release()
	_ = logFile.Close()

	result := shutdownCommandResult{Scheduled: true, WorkerPID: pid, LogPath: logPath, Failures: []app.ShutdownFailure{}}
	if jsonOut {
		return writeJSON(cfg.Stdout, result)
	}
	fmt.Fprintf(cfg.Stdout, "shutdown scheduled (worker pid %d)\n", pid)
	fmt.Fprintf(cfg.Stdout, "result log: %s\n", logPath)
	return nil
}

func shutdownWorkerEnvironment(entries []string) []string {
	scrub := map[string]bool{
		"WI_ID": true, "WI_DIR": true, "WI_WORKTREE": true, "WI_REPOSITORY": true, "WI_TMUX_CLIENT": true,
	}
	filtered := make([]string, 0, len(entries))
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || scrub[key] || strings.HasPrefix(key, "WI_AGENT_") || strings.HasPrefix(key, "PI_CODING_AGENT_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func shutdownOperationEnvironment(env map[string]string) map[string]string {
	filtered := make(map[string]string, len(env))
	for key, value := range env {
		if key == "TMUX" || key == "TMUX_PANE" || key == "WI_ID" || key == "WI_DIR" || key == "WI_WORKTREE" || key == "WI_REPOSITORY" || key == "WI_TMUX_CLIENT" || strings.HasPrefix(key, "WI_AGENT_") || strings.HasPrefix(key, "PI_CODING_AGENT_") {
			continue
		}
		filtered[key] = value
	}
	return filtered
}

func waitForDaemonExit(ctx context.Context, cfg Config, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		err := cfg.Coordinator.Ping(probeCtx)
		cancel()
		if err != nil {
			return true
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return false
}

func printShutdownResult(cfg Config, result shutdownCommandResult) {
	runtimes, terminals := 0, len(result.OrphanedClosed)
	for _, item := range result.Items {
		if item.RuntimeStopRequested {
			runtimes++
		}
		if item.TerminalClosed {
			terminals++
		}
		for _, warning := range item.Warnings {
			fmt.Fprintf(cfg.Stderr, "warning: %s: %s\n", item.WorkItemID, warning)
		}
	}
	fmt.Fprintf(cfg.Stdout, "agent runtimes stopped: %d\n", runtimes)
	fmt.Fprintf(cfg.Stdout, "wi tmux sessions closed: %d\n", terminals)
	if result.DaemonStopped {
		fmt.Fprintln(cfg.Stdout, "daemon stopped")
	}
	for _, failure := range result.Failures {
		resource := failure.Resource
		if failure.WorkItemID != "" {
			resource = failure.WorkItemID + " " + resource
		}
		fmt.Fprintf(cfg.Stderr, "error: %s: %s\n", resource, failure.Error)
	}
}
