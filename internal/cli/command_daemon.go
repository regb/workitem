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
	"strconv"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/apiview"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/daemon"
	"github.com/regb/workitem/internal/plugins/bundled"
	"github.com/regb/workitem/internal/version"
	"github.com/regb/workitem/internal/xdg"
)

// operatorCredential is how daemon management commands authenticate: they
// act for the operator.
func operatorCredential(paths xdg.Paths) apitransport.Credential {
	return credential.Source(credential.OperatorFile(paths.DataRoot), credential.Operator)
}

func probeDaemon(ctx context.Context, paths xdg.Paths, socketPath string) (coordinator.Status, error) {
	clients, err := apitransport.NewClients(socketPath, operatorCredential(paths))
	if err != nil {
		return coordinator.Status{}, err
	}
	defer clients.Close()
	value, err := clients.System.GetInfo(ctx, connect.NewRequest(&v1.GetInfoRequest{}))
	if err != nil {
		return coordinator.Status{}, err
	}
	pid, _ := strconv.Atoi(value.Header().Get("Wi-Daemon-Pid"))
	schema, _ := strconv.ParseUint(value.Header().Get("Wi-Database-Schema"), 10, 64)
	out := coordinator.Status{PID: pid, StartedAt: apiview.Time(value.Msg.StartedAt).Format(time.RFC3339Nano), DataRoot: value.Msg.DataRoot, SocketPath: socketPath, Access: value.Msg.Caller.GetPrincipal(), Managed: value.Msg.Managed, BuildIdentity: value.Header().Get("Wi-Build-Identity"), Database: coordinator.DatabaseStatus{SchemaVersion: schema}}
	if !strings.HasPrefix(value.Msg.ApiVersion, "1.") {
		return out, &coordinator.CompatibilityError{Kind: "API", Expected: "1", Actual: value.Msg.ApiVersion}
	}
	return out, nil
}

// ensureDaemonRunning controls the local service only. It never opens storage
// or invokes a domain use case in the client process.
func ensureDaemonRunning(ctx context.Context, paths xdg.Paths, socketPath string, managed bool) (coordinator.Status, bool, error) {
	probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	status, err := probeDaemon(probe, paths, socketPath)
	cancel()
	if err == nil {
		if status.BuildIdentity != "" && status.BuildIdentity != version.BuildIdentity() {
			return status, false, fmt.Errorf("daemon build differs; restart the daemon with this wi build")
		}
		return status, false, nil
	}
	if managed {
		return status, false, fmt.Errorf("the wi daemon is managed by a service manager; start or restart it there")
	}
	self, err := os.Executable()
	if err != nil {
		return status, false, err
	}
	if err := os.MkdirAll(paths.DataStateRoot, 0700); err != nil {
		return status, false, err
	}
	logPath := filepath.Join(paths.DataStateRoot, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return status, false, err
	}
	bootstrap, err := daemonBootstrapEnvironment(os.Environ())
	if err != nil {
		logFile.Close()
		return status, false, err
	}
	command := exec.Command(self, "daemon", "serve", "--json")
	command.Env = bootstrap
	command.Dir = paths.DataStateRoot
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = detachedProcessAttributes()
	err = command.Start()
	logFile.Close()
	if err != nil {
		return status, false, err
	}
	_ = command.Process.Release()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return status, true, err
		}
		probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		status, err = probeDaemon(probe, paths, socketPath)
		cancel()
		if err == nil {
			return status, true, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return status, true, fmt.Errorf("wi daemon did not become ready; inspect %s", logPath)
}
func runDaemonMain(ctx context.Context, args []string, paths xdg.Paths, stdout, stderr io.Writer, jsonOut bool) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: wi daemon <start|serve|status|stop|doctor>")
		return ExitUsage
	}
	socket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitError
	}
	fail := func(err error) int { fmt.Fprintln(stderr, err); return ExitError }
	switch args[0] {
	case "serve":
		signalCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		self, _ := os.Executable()
		cfg, host, _, err := bundled.Load(paths.ConfigFile, self)
		if err != nil {
			return fail(err)
		}
		d, err := daemon.New(signalCtx, daemon.Options{Paths: paths, Socket: socket, Config: cfg, Host: host, Self: self, Env: environMap(os.Environ())})
		if err != nil {
			return fail(err)
		}
		defer d.Close()
		if !jsonOut {
			fmt.Fprintf(stderr, "wi daemon API listening on %s\n", socket)
		}
		if err := d.Serve(signalCtx); err != nil {
			return fail(err)
		}
		return ExitOK
	case "start":
		cfg, err := config.LoadDaemon(paths.ConfigFile)
		if err != nil {
			return fail(err)
		}
		status, started, err := ensureDaemonRunning(ctx, paths, socket, cfg.Managed)
		if err != nil {
			return fail(err)
		}
		if jsonOut {
			clients, _ := apitransport.NewClients(socket, operatorCredential(paths))
			defer clients.Close()
			value, err := clients.System.GetInfo(ctx, connect.NewRequest(&v1.GetInfoRequest{}))
			if err != nil {
				return fail(err)
			}
			if err := rpcEmit(Config{Stdout: stdout}, true, value.Msg); err != nil {
				return fail(err)
			}
		} else {
			verb := "already running"
			if started {
				verb = "started"
			}
			fmt.Fprintf(stdout, "daemon %s (pid %d)\n", verb, status.PID)
		}
		return ExitOK
	case "stop":
		probe, cancel := context.WithTimeout(ctx, time.Second)
		status, err := probeDaemon(probe, paths, socket)
		cancel()
		if err != nil {
			return fail(err)
		}
		if status.Managed {
			return fail(errors.New(coordinator.ManagedDaemonMessage))
		}
		if status.PID <= 0 || status.PID == os.Getpid() {
			return fail(errors.New("daemon reported invalid PID"))
		}
		process, err := os.FindProcess(status.PID)
		if err != nil {
			return fail(err)
		}
		if err := process.Signal(syscall.SIGTERM); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "daemon stopping")
		return ExitOK
	case "status", "doctor":
		clients, err := apitransport.NewClients(socket, operatorCredential(paths))
		if err != nil {
			return fail(err)
		}
		defer clients.Close()
		cfg := Config{API: clients, Stdout: stdout, Stderr: stderr}
		if args[0] == "doctor" {
			value, err := clients.System.RunDiagnostics(ctx, connect.NewRequest(&v1.RunDiagnosticsRequest{}))
			if err != nil {
				return fail(err)
			}
			if err := rpcEmit(cfg, jsonOut, value.Msg); err != nil {
				return fail(err)
			}
		} else {
			value, err := clients.System.GetHealth(ctx, connect.NewRequest(&v1.GetHealthRequest{}))
			if err != nil {
				return fail(err)
			}
			if err := rpcEmit(cfg, jsonOut, value.Msg); err != nil {
				return fail(err)
			}
		}
		return ExitOK
	}
	return fail(fmt.Errorf("unknown daemon command %q", args[0]))
}
