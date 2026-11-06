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
	"github.com/regb/workitem/internal/daemonapi"
	"github.com/regb/workitem/internal/daemonapp"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/plugins/bundled"
	"github.com/regb/workitem/internal/version"
	"github.com/regb/workitem/internal/xdg"
)

func daemonCommandRequested(args []string) bool {
	filtered, _ := extractGlobals(args)
	return len(filtered) > 0 && filtered[0] == "daemon"
}
func probeDaemon(ctx context.Context, socketPath string) (coordinator.Status, error) {
	clients, err := apitransport.NewClients(socketPath)
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
	out := coordinator.Status{PID: pid, StartedAt: apiview.Time(value.Msg.StartedAt).Format(time.RFC3339Nano), DataRoot: value.Msg.DataRoot, SocketPath: socketPath, Access: callerAccess(value.Msg.Caller), Managed: value.Msg.Managed, BuildIdentity: value.Header().Get("Wi-Build-Identity"), Database: coordinator.DatabaseStatus{SchemaVersion: schema}}
	if !strings.HasPrefix(value.Msg.ApiVersion, "1.") {
		return out, &coordinator.CompatibilityError{Kind: "API", Expected: "1", Actual: value.Msg.ApiVersion}
	}
	return out, nil
}

// ensureDaemonRunning controls the local service only. It never opens storage
// or invokes a domain use case in the client process.
func ensureDaemonRunning(ctx context.Context, paths xdg.Paths, socketPath string, managed bool) (coordinator.Status, bool, error) {
	probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	status, err := probeDaemon(probe, socketPath)
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
		status, err = probeDaemon(probe, socketPath)
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
	agentSocket, err := coordinator.AgentSocketPath(paths.RuntimeDir, paths.DataRoot)
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
		decoders := coordinator.ConversationDecoders{}
		for name, configured := range configuredDriverClients(cfg, self, environMap(os.Environ())) {
			client := configured
			decoders[name] = func(ctx context.Context, conversation model.Conversation, records []coordinator.ConversationRecord) ([]coordinator.ConversationEventFact, []bool, error) {
				lines, numbers := make([][]byte, len(records)), make([]uint64, len(records))
				for i, r := range records {
					lines[i], numbers[i] = r.Raw, r.Line
				}
				facts, decoded, err := client.DecodeConversation(ctx, conversation, numbers, lines)
				if err != nil {
					return nil, nil, err
				}
				converted := make([]coordinator.ConversationEventFact, len(facts))
				for i, f := range facts {
					converted[i] = coordinator.ConversationEventFact{Line: f.Line, Type: f.Type, Role: f.Role, Timestamp: f.Timestamp, StopReason: f.StopReason, ContentTypes: f.ContentTypes, Terminal: f.Terminal, Failed: f.Failed}
				}
				return converted, decoded, nil
			}
		}
		core, err := coordinator.NewServerWithAgentSocket(paths.DataRoot, socket, agentSocket, decoders)
		if err != nil {
			return fail(err)
		}
		defer core.Close()
		core.ConfigureRepositorySettings(host.Registry.Options())
		core.SetManaged(cfg.Daemon.Managed)
		application, err := daemonapp.New(signalCtx, core, paths, cfg, host, self, environMap(os.Environ()))
		if err != nil {
			return fail(err)
		}
		application.DaemonSocketPath = coordinator.InternalSocketPath(socket)
		api, err := daemonapi.New(signalCtx, core, application, cfg, host, paths.DataRoot)
		if err != nil {
			return fail(err)
		}
		core.SetBeforeClose(api.Close)
		pluginCtx, cancelPlugins := context.WithCancel(signalCtx)
		defer cancelPlugins()
		pluginsDone := make(chan struct{})
		go func() {
			defer close(pluginsDone)
			plugin.Runner{Host: host, WiPath: self, LogDir: filepath.Join(paths.DataStateRoot, "plugins")}.Run(pluginCtx)
		}()
		if !jsonOut {
			fmt.Fprintf(stderr, "wi daemon API listening on %s\n", socket)
		}
		err = core.ServeAPI(signalCtx, api.Handler())
		cancelPlugins()
		<-pluginsDone
		if err != nil {
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
			clients, _ := apitransport.NewClients(socket)
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
		status, err := probeDaemon(probe, socket)
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
		clients, err := apitransport.NewClients(socket)
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
