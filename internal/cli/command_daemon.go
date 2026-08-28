package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/xdg"
)

func daemonCommandRequested(args []string) bool {
	filtered, _ := extractGlobals(args)
	return len(filtered) > 0 && filtered[0] == "daemon"
}

func ensureDaemonRunning(ctx context.Context, paths xdg.Paths, socketPath string) (coordinator.Status, bool, error) {
	client := &coordinator.Client{SocketPath: socketPath}
	probeCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	status, err := client.Status(probeCtx)
	cancel()
	if err == nil {
		return status, false, nil
	}
	var compatibility *coordinator.CompatibilityError
	if errors.As(err, &compatibility) {
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 2*time.Second)
		if compatibility.Kind == "build" {
			err = client.Shutdown(shutdownCtx)
		} else if compatibility.Kind == "protocol" && compatibility.Actual == fmt.Sprint(coordinator.ProtocolVersion-1) {
			err = client.ShutdownProtocol(shutdownCtx, coordinator.ProtocolVersion-1)
		} else {
			err = fmt.Errorf("cannot automatically replace incompatible daemon: %w", compatibility)
		}
		shutdownCancel()
		if err != nil {
			return coordinator.Status{}, false, err
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			waitCtx, waitCancel := context.WithTimeout(ctx, 100*time.Millisecond)
			waitErr := client.Ping(waitCtx)
			waitCancel()
			if waitErr != nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	self, err := os.Executable()
	if err != nil {
		return coordinator.Status{}, false, fmt.Errorf("resolve wi executable: %w", err)
	}
	if err := os.MkdirAll(paths.DataStateRoot, 0o700); err != nil {
		return coordinator.Status{}, false, fmt.Errorf("create daemon state directory: %w", err)
	}
	if err := os.Chmod(paths.DataStateRoot, 0o700); err != nil {
		return coordinator.Status{}, false, fmt.Errorf("secure daemon state directory: %w", err)
	}
	logPath := filepath.Join(paths.DataStateRoot, "daemon.log")
	if info, statErr := os.Stat(logPath); statErr == nil && info.Size() > 4<<20 {
		_ = os.Remove(logPath + ".1")
		_ = os.Rename(logPath, logPath+".1")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return coordinator.Status{}, false, fmt.Errorf("open daemon log: %w", err)
	}
	command := exec.Command(self, "daemon", "serve", "--json")
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = detachedProcessAttributes()
	if err := command.Start(); err != nil {
		logFile.Close()
		return coordinator.Status{}, false, fmt.Errorf("start wi daemon: %w", err)
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return coordinator.Status{}, true, err
		}
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		status, err = client.Status(requestCtx)
		requestCancel()
		if err == nil {
			return status, true, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return coordinator.Status{}, true, fmt.Errorf("wi daemon did not become ready; inspect %s", logPath)
}

func runDaemonMain(ctx context.Context, args []string, paths xdg.Paths, stdout, stderr io.Writer, jsonOut bool) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: wi daemon <start|serve|status|stop|doctor>")
		return ExitUsage
	}
	socketPath, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitError
	}
	agentSocketPath, err := coordinator.AgentSocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitError
	}
	switch args[0] {
	case "start":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: wi daemon start")
			return ExitUsage
		}
		status, started, err := ensureDaemonRunning(ctx, paths, socketPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return ExitError
		}
		if jsonOut {
			if err := writeJSON(stdout, map[string]any{"started": started, "status": status}); err != nil {
				fmt.Fprintln(stderr, err)
				return ExitError
			}
		} else if started {
			fmt.Fprintf(stdout, "daemon started (pid %d)\n", status.PID)
		} else {
			fmt.Fprintf(stdout, "daemon already running (pid %d)\n", status.PID)
		}
		return ExitOK
	case "serve":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: wi daemon serve")
			return ExitUsage
		}
		signalCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		server, err := coordinator.NewServerWithAgentSocket(paths.DataRoot, socketPath, agentSocketPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return ExitError
		}
		defer server.Close()
		if !jsonOut {
			fmt.Fprintf(stderr, "wi daemon listening on %s (agent endpoint %s)\n", socketPath, agentSocketPath)
		}
		if err := server.Serve(signalCtx); err != nil {
			fmt.Fprintln(stderr, err)
			return ExitError
		}
		return ExitOK
	case "status":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: wi daemon status")
			return ExitUsage
		}
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status, err := (&coordinator.Client{SocketPath: socketPath}).Status(requestCtx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return ExitError
		}
		if jsonOut {
			if err := writeJSON(stdout, status); err != nil {
				fmt.Fprintln(stderr, err)
				return ExitError
			}
			return ExitOK
		}
		fmt.Fprintf(stdout, "daemon: running (pid %d)\n", status.PID)
		fmt.Fprintf(stdout, "socket: %s\n", status.SocketPath)
		fmt.Fprintf(stdout, "agent socket: %s\n", status.AgentSocketPath)
		fmt.Fprintf(stdout, "database: %s (schema %d, sequence %d)\n", status.Database.Path, status.Database.SchemaVersion, status.Database.GlobalSequence)
		return ExitOK
	case "doctor":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: wi daemon doctor")
			return ExitUsage
		}
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		status, err := (&coordinator.Client{SocketPath: socketPath}).Status(requestCtx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return ExitError
		}
		if jsonOut {
			if err := writeJSON(stdout, status); err != nil {
				fmt.Fprintln(stderr, err)
				return ExitError
			}
		} else {
			fmt.Fprintf(stdout, "database schema: %d\n", status.Database.SchemaVersion)
			fmt.Fprintf(stdout, "global sequence: %d\n", status.Database.GlobalSequence)
			fmt.Fprintf(stdout, "Pi session sources: %d files, %d entries, %d warning(s)\n", status.PiImport.FilesSeen, status.PiImport.EntriesScanned, len(status.PiImport.Warnings))
			for _, warning := range status.PiImport.Warnings {
				fmt.Fprintf(stderr, "warning: %s\n", warning)
			}
		}
		if len(status.PiImport.Warnings) > 0 {
			return ExitError
		}
		return ExitOK
	case "stop":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: wi daemon stop")
			return ExitUsage
		}
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		client := &coordinator.Client{SocketPath: socketPath}
		if err := client.Shutdown(requestCtx); err != nil {
			var compatibility *coordinator.CompatibilityError
			if !errors.As(err, &compatibility) || compatibility.Kind != "protocol" || compatibility.Actual != fmt.Sprint(coordinator.ProtocolVersion-1) {
				fmt.Fprintln(stderr, err)
				return ExitError
			}
			if err := client.ShutdownProtocol(requestCtx, coordinator.ProtocolVersion-1); err != nil {
				fmt.Fprintln(stderr, err)
				return ExitError
			}
		}
		if !jsonOut {
			fmt.Fprintln(stdout, "daemon stopping")
		}
		return ExitOK
	default:
		fmt.Fprintln(stderr, errors.New("unknown daemon subcommand "+args[0]))
		return ExitUsage
	}
}
