package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/daemonapp"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/plugins/bundled"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/xdg"
)

const (
	ExitOK        = 0
	ExitError     = 1
	ExitUsage     = 2
	ExitNotFound  = 3
	ExitAmbiguous = 4
)

type Config struct {
	Stdout    io.Writer
	Stderr    io.Writer
	Stdin     io.Reader
	CWD       string
	Env       map[string]string
	StateRoot string
	Paths     xdg.Paths
	// Testable client-side terminal detection and attachment, never daemon use cases.
	Interactive   func() bool
	RunTerminal   func(context.Context, []string) error
	API           *apitransport.Clients
	SocketPath    string
	GlobalConfig  *config.Config
	ControlAccess string
	Plugins       *plugin.Host
}

func Main(ctx context.Context, args []string) int {
	env := environMap(os.Environ())
	// The picker's formatting callback reads only its frozen snapshot; it
	// must not contact or start the daemon.
	if len(args) > 0 && args[0] == "internal-picker-render" {
		return runPickerRender(args[1:], env, os.Stdout, os.Stderr)
	}
	filtered, jsonOut := extractGlobals(args)
	if len(filtered) == 0 || filtered[0] == "help" {
		return Run(ctx, args, Config{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Env: env})
	}
	if _, help := requestedHelpTopic(filtered); help {
		return Run(ctx, args, Config{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Env: env})
	}
	paths, err := xdg.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitError
	}
	if filtered[0] == "daemon" {
		return runDaemonMain(ctx, filtered[1:], paths, os.Stdout, os.Stderr, jsonOut)
	}
	self, _ := os.Executable()
	cfgFile, host, warnings, err := bundled.Load(paths.ConfigFile, self)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitError
	}
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", warning)
	}
	cwd, _ := os.Getwd()
	if len(filtered) > 1 && filtered[0] == "agent" && filtered[1] == "exec" {
		socket := coordinator.InternalSocketPath(mustOperatorSocket(paths))
		if value := env["WI_DAEMON_SOCKET"]; value != "" {
			socket = value
		}
		err := daemonapp.ExecHost(ctx, filtered[2:], &coordinator.Client{SocketPath: socket}, paths, cfgFile, host, self, env)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return ExitError
		}
		return ExitOK
	}
	socket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitError
	}
	agentSocket := strings.TrimSpace(env["WI_AGENT_DAEMON_SOCKET"])
	if agentSocket != "" {
		if !filepath.IsAbs(agentSocket) {
			fmt.Fprintln(os.Stderr, "WI_AGENT_DAEMON_SOCKET must be absolute")
			return ExitError
		}
		socket = agentSocket
	}
	clients, err := apitransport.NewClients(socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitError
	}
	defer clients.Close()
	probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	info, probeErr := clients.System.GetInfo(probe, connect.NewRequest(&v1.GetInfoRequest{}))
	cancel()
	if probeErr != nil && agentSocket == "" {
		if _, _, err := ensureDaemonRunning(ctx, paths, socket, cfgFile.Daemon.Managed); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return ExitError
		}
		info, probeErr = clients.System.GetInfo(ctx, connect.NewRequest(&v1.GetInfoRequest{}))
	}
	if probeErr != nil {
		fmt.Fprintln(os.Stderr, "daemon API is unavailable:", probeErr)
		return ExitError
	}
	access := callerAccess(info.Msg.Caller)
	if agentSocket != "" && access != "agent" {
		fmt.Fprintln(os.Stderr, "configured agent socket is not a restricted endpoint")
		return ExitError
	}
	if !strings.HasPrefix(info.Msg.ApiVersion, "1.") {
		fmt.Fprintln(os.Stderr, "unsupported daemon API version:", info.Msg.ApiVersion)
		return ExitError
	}
	host.ReadRepository = func(ctx context.Context, id string) (model.RepositorySettings, error) {
		value, err := clients.Repositories.GetRepositorySettings(ctx, connect.NewRequest(&v1.GetRepositorySettingsRequest{RepositoryId: id, ShowSensitive: true}))
		if err != nil {
			return model.RepositorySettings{}, err
		}
		out := model.RepositorySettings{RepositoryID: id, Revision: uint64(value.Msg.Revision), Namespaces: map[string]map[string]json.RawMessage{}}
		for _, setting := range value.Msg.Settings {
			parts := strings.Split(setting.Key, ".")
			if len(parts) != 3 {
				continue
			}
			namespace := parts[0] + "." + parts[1]
			if out.Namespaces[namespace] == nil {
				out.Namespaces[namespace] = map[string]json.RawMessage{}
			}
			raw, _ := json.Marshal(cliSettingAny(setting.Effective))
			out.Namespaces[namespace][parts[2]] = raw
		}
		return out, nil
	}
	host.ListRepositories = func(ctx context.Context) ([]string, error) {
		value, err := clients.Repositories.ListRepositories(ctx, connect.NewRequest(&v1.ListRepositoriesRequest{}))
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, repo := range value.Msg.Repositories {
			ids = append(ids, repo.Id)
		}
		return ids, nil
	}
	return Run(ctx, args, Config{Paths: paths, API: clients, SocketPath: socket, CWD: cwd, Env: env, StateRoot: paths.DataStateRoot, GlobalConfig: &cfgFile, Plugins: host, ControlAccess: access, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
}
func callerAccess(caller *v1.Caller) string {
	if caller == nil {
		return ""
	}
	switch caller.Access {
	case v1.CallerAccess_CALLER_ACCESS_OPERATOR:
		return coordinator.AccessOperator
	case v1.CallerAccess_CALLER_ACCESS_AGENT:
		return coordinator.AccessAgent
	}
	return ""
}
func mustOperatorSocket(paths xdg.Paths) string {
	value, _ := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	return value
}
func cliSettingAny(value *v1.SettingValue) any {
	if value == nil {
		return nil
	}
	switch v := value.Value.(type) {
	case *v1.SettingValue_StringValue:
		return v.StringValue
	case *v1.SettingValue_BoolValue:
		return v.BoolValue
	case *v1.SettingValue_IntValue:
		return v.IntValue
	case *v1.SettingValue_ListValue:
		// Protobuf decodes an empty repeated field as a nil slice. Preserve
		// the list's type so JSON round trips emit [] rather than null.
		values := []string{}
		if v.ListValue != nil {
			values = append(values, v.ListValue.Values...)
		}
		return values
	}
	return nil
}

func extractGlobals(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	jsonOut := false
	literal := false
	for _, arg := range args {
		if arg == "--" {
			literal = true
			out = append(out, arg)
			continue
		}
		if !literal && arg == "--json" {
			jsonOut = true
			continue
		}
		out = append(out, arg)
	}
	return out, jsonOut
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		if !printHelp(stderr, name) {
			fmt.Fprintf(stderr, "Usage: wi %s [options]\n", name)
			fs.PrintDefaults()
		}
	}
	return fs
}

func itemSelectorFromFlag(fs *flag.FlagSet, item string) (string, error) {
	flagSelector := strings.TrimSpace(item)
	positionalSelector := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if flagSelector != "" && positionalSelector != "" {
		return "", fmt.Errorf("pass item either with --item or as a positional argument, not both")
	}
	if flagSelector != "" {
		return flagSelector, nil
	}
	return positionalSelector, nil
}

type repeatFlag []string

func (r *repeatFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatFlag) Set(s string) error {
	*r = append(*r, s)
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

type usageErr struct{ err error }

func (e usageErr) Error() string { return e.err.Error() }
func (e usageErr) Unwrap() error { return e.err }

func exitCode(err error) int {
	var amb store.AmbiguousError
	var configKey configKeyNotFoundError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &amb):
		return ExitAmbiguous
	case errors.As(err, &configKey):
		return ExitNotFound
	case errors.Is(err, store.ErrNotFound):
		return ExitNotFound
	}
	var u usageErr
	if errors.As(err, &u) {
		return ExitUsage
	}
	return ExitError
}

func environMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

const timeFormatHuman = "2006-01-02 15:04:05 MST"
