package cli

import (
	"bufio"
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

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/extension"
	"github.com/regb/workitem/internal/extensions/bundled"
	"github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/tmux"
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
	RequireOwnedRepository bool
	Stdout                 io.Writer
	Stderr                 io.Writer
	Stdin                  io.Reader
	CWD                    string
	Env                    map[string]string
	StateRoot              string
	App                    *app.App
	Coordinator            *coordinator.Client
	GlobalConfig           *config.Config
	ControlAccess          string
}

func Main(ctx context.Context, args []string) int {
	env := environMap(os.Environ())
	if len(args) > 0 && args[0] == "internal-picker-render" {
		return runPickerRender(args[1:], env, os.Stdout, os.Stderr)
	}
	filtered, jsonOut := extractGlobals(args)
	_, helpRequested := requestedHelpTopic(filtered)
	if len(filtered) == 0 || filtered[0] == "help" || helpRequested {
		return Run(ctx, args, Config{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Env: env})
	}
	if versionCommandRequested(args) {
		return Run(ctx, args, Config{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Env: env})
	}
	paths, err := xdg.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitError
	}
	if filtered[0] == "config" {
		return runConfigMain(filtered[1:], paths.ConfigFile, os.Stdout, os.Stderr, jsonOut)
	}
	if filtered[0] == "init" {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return ExitError
		}
		return runInitMain(ctx, filtered[1:], paths.ConfigFile, self, env, os.Stdin, os.Stdout, os.Stderr, jsonOut)
	}
	if infoCommandRequested(args) {
		filtered, jsonOut := extractGlobals(args)
		return runInfoMain(filtered[1:], paths, os.Stdout, os.Stderr, jsonOut)
	}
	if daemonCommandRequested(args) {
		filtered, jsonOut := extractGlobals(args)
		return runDaemonMain(ctx, filtered[1:], paths, os.Stdout, os.Stderr, jsonOut)
	}
	cfgFile, warnings, err := config.LoadWithRegistry(paths.ConfigFile, bundled.Registry())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", paths.ConfigFile, err)
		return ExitError
	}
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "warning: config: %s\n", warning)
	}
	st := store.New(paths.DataRoot)
	if err := st.Ensure(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitError
	}
	application := app.New(st, git.New("git"))
	application.DeepWorkConfig = cfgFile.DeepWork
	application.ItemConfig = cfgFile.Item
	application.ListConfig = cfgFile.List
	application.AgentConfig = cfgFile.Agent
	application.AgentStatusConfig = cfgFile.AgentStatus
	application.AttentionConfig = cfgFile.Attention
	application.DefaultAgent = cfgFile.DefaultAgent
	application.AgentsConfig = cfgFile.Agents
	application.DriversConfig = cfgFile.Drivers
	var approve extension.Approver
	if !jsonOut && inputIsTerminal(os.Stdin) {
		approve = func(ctx context.Context, request extension.Approval) (bool, error) {
			return promptExtensionApproval(os.Stdin, os.Stderr, request)
		}
	}
	mux := tmux.New("tmux")
	application.Tmux = mux
	if self, err := os.Executable(); err == nil {
		application.SelfPath = self
		for _, driver := range []string{"pi", "claude"} {
			application.BundledDriverCommands[driver] = app.BundledDriverCommand{Path: self, Args: []string{"internal-driver", driver}}
		}
	}
	for driverName, client := range configuredDriverClients(cfgFile, application.SelfPath, env) {
		application.ConversationBackends[driverName] = client
	}
	cwd, _ := os.Getwd()
	application.AgentRuntimeStateRoot = paths.DataStateRoot
	application.AgentRuntimeSocketRoot = paths.DataRuntimeRoot
	runConfig := Config{RequireOwnedRepository: true, Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, CWD: cwd, Env: env, StateRoot: paths.DataStateRoot, App: application, GlobalConfig: &cfgFile}
	defaultSocketPath, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon is required for this command: %s\n", err)
		return ExitError
	}
	agentSocketPath := strings.TrimSpace(env["WI_AGENT_DAEMON_SOCKET"])
	if agentSocketPath != "" && !filepath.IsAbs(agentSocketPath) {
		fmt.Fprintln(os.Stderr, "WI_AGENT_DAEMON_SOCKET must be an absolute path")
		return ExitError
	}
	socketPath := defaultSocketPath
	if agentSocketPath != "" {
		socketPath = agentSocketPath
	}
	client := &coordinator.Client{SocketPath: socketPath}
	probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	status, probeErr := client.Status(probeCtx)
	cancel()
	if probeErr == nil {
		if agentSocketPath != "" && status.Access != coordinator.AccessAgent {
			fmt.Fprintln(os.Stderr, "WI_AGENT_DAEMON_SOCKET does not identify a restricted agent endpoint")
			return ExitError
		}
		runConfig.Coordinator = client
		runConfig.ControlAccess = status.Access
	} else if agentSocketPath == "" {
		status, _, startErr := ensureDaemonRunning(ctx, paths, socketPath)
		if startErr != nil {
			fmt.Fprintf(os.Stderr, "daemon is required for this command: %s\n", startErr)
			return ExitError
		}
		runConfig.Coordinator = client
		runConfig.ControlAccess = status.Access
	} else {
		fmt.Fprintf(os.Stderr, "configured agent endpoint is unavailable: %s\n", probeErr)
		return ExitError
	}
	BindCoordinatorApp(application, st, runConfig.Coordinator)
	application.Extensions = bundled.New(cfgFile, approve, func(ctx context.Context, id string) (model.RepositorySettings, error) {
		result, err := client.RepositorySettings(ctx, id)
		return result.Settings, err
	})
	if commandUsesCLIProjectEnvironment(args) {
		repositoryID, boundary := "", ""
		if current, findErr := application.Store.FindByWorktree(cwd); findErr == nil {
			repositoryID = current.Repository.ID
			if current.Checkout.Claim != nil {
				boundary = current.Checkout.Claim.Path
			}
		}
		var projectWarnings []string
		env, projectWarnings, err = loadCLIProjectEnvironment(ctx, application.Extensions, cwd, boundary, repositoryID, env)
		for _, warning := range projectWarnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return ExitError
		}
		runConfig.Env = env
	}
	return Run(ctx, args, runConfig)
}

func Run(ctx context.Context, args []string, cfg Config) int {
	if cfg.Coordinator == nil && cfg.App != nil && strings.TrimSpace(cfg.App.DaemonSocketPath) != "" {
		cfg.Coordinator = &coordinator.Client{SocketPath: cfg.App.DaemonSocketPath}
		if native, ok := cfg.App.Store.(*store.Store); ok {
			BindCoordinatorApp(cfg.App, native, cfg.Coordinator)
		}
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	if cfg.Stdin == nil {
		cfg.Stdin = strings.NewReader("")
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	args, jsonOut := extractGlobals(args)
	if len(args) == 0 {
		printUsage(cfg.Stdout)
		return ExitOK
	}
	if args[0] == "help" {
		topic := strings.Join(args[1:], " ")
		if !printHelp(cfg.Stdout, topic) {
			fmt.Fprintf(cfg.Stderr, "unknown help topic %q\n", topic)
			return ExitUsage
		}
		return ExitOK
	}
	if topic, requested := requestedHelpTopic(args); requested {
		if !printHelp(cfg.Stdout, topic) {
			fmt.Fprintf(cfg.Stderr, "unknown help topic %q\n", topic)
			return ExitUsage
		}
		return ExitOK
	}
	if args[0] == "version" {
		if err := runVersion(args[1:], cfg, jsonOut); err != nil {
			fmt.Fprintln(cfg.Stderr, err)
			return exitCode(err)
		}
		return ExitOK
	}
	if cfg.App == nil {
		fmt.Fprintln(cfg.Stderr, "internal error: app is not configured")
		return ExitError
	}

	cmd := args[0]
	var err error
	switch cmd {
	case "repo":
		err = runRepository(ctx, args[1:], cfg, jsonOut)
	case "new":
		err = runNew(ctx, args[1:], cfg, jsonOut)
	case "list":
		err = runList(ctx, args[1:], cfg, jsonOut)
	case "show":
		err = runShow(ctx, args[1:], cfg, jsonOut)
	case "events":
		err = runEvents(ctx, args[1:], cfg, jsonOut)
	case "merge":
		err = runMerge(ctx, args[1:], cfg, jsonOut)
	case "state":
		err = runState(ctx, args[1:], cfg, jsonOut)
	case "attention":
		err = runAttention(ctx, args[1:], cfg, jsonOut)
	case "workspace":
		err = runWorkspace(ctx, args[1:], cfg, jsonOut)
	case "terminal":
		err = runTerminal(ctx, args[1:], cfg, jsonOut)
	case "start":
		err = runStart(ctx, args[1:], cfg, jsonOut)
	case "switch":
		err = runSwitch(ctx, args[1:], cfg, jsonOut)
	case "next":
		err = runNext(ctx, args[1:], cfg, jsonOut)
	case "resume":
		err = runResume(ctx, args[1:], cfg, jsonOut)
	case "shelve":
		err = runShelve(ctx, args[1:], cfg, jsonOut)
	case "close":
		err = runClose(ctx, args[1:], cfg, jsonOut)
	case "wait":
		err = runWait(ctx, args[1:], cfg, jsonOut)
	case "pick":
		err = runPick(ctx, args[1:], cfg, jsonOut)
	case "blocker":
		err = runBlocker(ctx, args[1:], cfg, jsonOut)
	case "delete":
		err = runDelete(ctx, args[1:], cfg, jsonOut)
	case "shutdown":
		err = runShutdown(ctx, args[1:], cfg, jsonOut)
	case "edit":
		err = runEdit(ctx, args[1:], cfg, jsonOut)
	case "agent":
		err = runAgent(ctx, args[1:], cfg, jsonOut)
	default:
		fmt.Fprintf(cfg.Stderr, "unknown command %q\n", cmd)
		return ExitUsage
	}
	if err != nil {
		fmt.Fprintln(cfg.Stderr, err)
		return exitCode(err)
	}
	return ExitOK
}

func effectiveListLabelRules(configRules []string, env map[string]string, cliRules []string) (map[string]bool, error) {
	rules := map[string]bool{}
	if err := applyListLabelRules(rules, configRules, "config"); err != nil {
		return nil, err
	}
	if raw, ok := env["WI_LIST_LABELS"]; ok {
		if err := applyListLabelRules(rules, []string{raw}, "WI_LIST_LABELS"); err != nil {
			return nil, err
		}
	}
	if err := applyListLabelRules(rules, cliRules, "--label"); err != nil {
		return nil, err
	}
	return rules, nil
}

func applyListLabelRules(effective map[string]bool, values []string, source string) error {
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if token == "!" {
				clear(effective)
				continue
			}
			include := true
			if token[0] == '+' || token[0] == '-' {
				include = token[0] != '-'
				token = strings.TrimSpace(token[1:])
			}
			label, err := model.NormalizeLabel(token)
			if err != nil {
				return fmt.Errorf("invalid %s label rule %q: %w", source, token, err)
			}
			effective[label] = include
		}
	}
	return nil
}

func printTransition(w io.Writer, res app.StateTransitionResult) {
	if res.Changed {
		fmt.Fprintf(w, "%s: %s -> %s\n", res.WorkItemID, res.PreviousState, res.State)
	} else {
		fmt.Fprintf(w, "%s: already %s\n", res.WorkItemID, res.State)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func runAgentExec(ctx context.Context, args []string, cfg Config) error {
	fs := newFlagSet("agent exec", cfg.Stderr)
	var itemID, conversationID, runtimeID, modeValue string
	fs.StringVar(&itemID, "item", "", "work-item ID")
	fs.StringVar(&conversationID, "conversation", "", "primary conversation ID")
	fs.StringVar(&runtimeID, "runtime", "", "agent runtime ID")
	fs.StringVar(&modeValue, "mode", "", "agent runtime mode")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if itemID == "" || conversationID == "" || runtimeID == "" || modeValue == "" {
		return usageErr{errors.New("usage: wi agent exec --item <id> --conversation <conversation-id> --runtime <runtime-id> --mode <tui|headless>")}
	}
	mode, err := agent.ParseMode(modeValue)
	if err != nil {
		return usageErr{err}
	}
	return cfg.App.AgentExec(ctx, itemID, conversationID, runtimeID, mode)
}

func versionCommandRequested(args []string) bool {
	filtered, _ := extractGlobals(args)
	return len(filtered) > 0 && filtered[0] == "version"
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

type newWorkItemCLIResult struct {
	WorkItemID      string          `json:"work_item_id"`
	ChangedArtifact string          `json:"changed_artifact"`
	State           string          `json:"state"`
	ItemDir         string          `json:"item_dir,omitempty"`
	Warnings        []string        `json:"warnings"`
	Manifest        *model.Manifest `json:"manifest,omitempty"`
	Checkout        *model.Checkout `json:"checkout,omitempty"`
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

func inputIsTerminal(input *os.File) bool {
	if input == nil {
		return false
	}
	info, err := input.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func promptExtensionApproval(input io.Reader, output io.Writer, request extension.Approval) (bool, error) {
	fmt.Fprintf(output, "%s\nPath: %s\nItem: %s [y/N] ", request.Question, request.Path, request.ItemID)
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
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
