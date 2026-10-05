package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/regb/workitem/internal/model"
)

type SessionSpec struct {
	Name  string
	CWD   string
	Env   map[string]string
	Scrub []string
}

type LaunchSpec struct {
	SessionName      string
	WindowName       string
	CWD              string
	Command          []string
	Env              map[string]string
	Scrub            []string
	ReuseAgentWindow bool
}

type PaneInfo = model.TerminalPaneInfo

type Runner interface {
	Run(ctx context.Context, path string, args []string, env []string) ([]byte, error)
}

type InteractiveRunner interface {
	RunInteractive(ctx context.Context, path string, args []string, env []string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, path string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	if env != nil {
		cmd.Env = env
	}
	return cmd.CombinedOutput()
}

func (ExecRunner) RunInteractive(ctx context.Context, path string, args []string, env []string) error {
	stdin, stdout, stderr, cleanup, err := interactiveStdio()
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, path, args...)
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

type Client struct {
	Path       string
	SocketPath string
	ConfigPath string
	Runner     Runner
}

func New(path string) Client {
	return NewWithServer(path, "", "")
}

func NewWithServer(path, socketPath, configPath string) Client {
	if path == "" {
		path = "tmux"
	}
	return Client{Path: path, SocketPath: socketPath, ConfigPath: configPath, Runner: ExecRunner{}}
}

func (c Client) serverArgs(args []string) []string {
	prefix := []string{}
	if strings.TrimSpace(c.SocketPath) != "" {
		prefix = append(prefix, "-S", c.SocketPath)
	}
	if strings.TrimSpace(c.ConfigPath) != "" {
		prefix = append(prefix, "-f", c.ConfigPath)
	}
	return append(prefix, args...)
}

func (c Client) HasSession(ctx context.Context, name string) (bool, error) {
	_, err := c.run(ctx, []string{"has-session", "-t", name}, nil)
	if err == nil {
		return true, nil
	}
	if isExitError(err) {
		return false, nil
	}
	return false, err
}

// EnsureSession creates a simple two-window tmux session when it does not already exist.
// It returns true when it created the session and false when an existing session was reused.
func (c Client) EnsureSession(ctx context.Context, spec SessionSpec) (bool, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return false, fmt.Errorf("tmux session name is required")
	}
	if strings.TrimSpace(spec.CWD) == "" {
		return false, fmt.Errorf("tmux session working directory is required")
	}
	exists, err := c.HasSession(ctx, spec.Name)
	if err != nil {
		return false, err
	}
	if exists {
		if err := c.applySessionEnvironment(ctx, spec.Name, spec.Env, spec.Scrub); err != nil {
			return false, err
		}
		return false, nil
	}

	cmdEnv := mergeEnv(envWithoutKeys(os.Environ(), scrubSet(spec.Scrub)), spec.Env)
	if _, err := c.run(ctx, []string{"new-session", "-d", "-s", spec.Name, "-c", spec.CWD, "-n", "agent"}, cmdEnv); err != nil {
		return false, err
	}
	if err := c.applySessionEnvironment(ctx, spec.Name, spec.Env, spec.Scrub); err != nil {
		return true, err
	}
	if _, err := c.run(ctx, []string{"new-window", "-t", spec.Name + ":", "-n", "shell", "-c", spec.CWD}, nil); err != nil {
		return true, err
	}
	if _, err := c.run(ctx, []string{"select-window", "-t", spec.Name + ":agent"}, nil); err != nil {
		return true, err
	}
	return true, nil
}

// EnsureCommandSession creates a one-window session whose first pane runs command.
// Existing sessions are reused without restarting their pane.
func (c Client) EnsureCommandSession(ctx context.Context, spec SessionSpec, windowName string, command []string) (bool, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return false, fmt.Errorf("tmux session name is required")
	}
	if strings.TrimSpace(spec.CWD) == "" {
		return false, fmt.Errorf("tmux session working directory is required")
	}
	if len(command) == 0 {
		return false, fmt.Errorf("command is required")
	}
	exists, err := c.HasSession(ctx, spec.Name)
	if err != nil {
		return false, err
	}
	if exists {
		if err := c.applySessionEnvironment(ctx, spec.Name, spec.Env, spec.Scrub); err != nil {
			return false, err
		}
		return false, nil
	}
	if strings.TrimSpace(windowName) == "" {
		windowName = "agent"
	}
	shellCommand, err := ShellCommand(command)
	if err != nil {
		return false, err
	}
	cmdEnv := mergeEnv(envWithoutKeys(os.Environ(), scrubSet(spec.Scrub)), spec.Env)
	if _, err := c.run(ctx, []string{"new-session", "-d", "-s", spec.Name, "-c", spec.CWD, "-n", windowName, "sleep 2147483647"}, cmdEnv); err != nil {
		return false, err
	}
	if err := c.applySessionEnvironment(ctx, spec.Name, spec.Env, spec.Scrub); err != nil {
		return true, err
	}
	if _, err := c.run(ctx, []string{"respawn-pane", "-k", "-c", spec.CWD, "-t", spec.Name + ":" + windowName, shellCommand}, nil); err != nil {
		return true, err
	}
	return true, nil
}

// applySessionEnvironment sets the requested environment on a session and marks
// scrubbed variables for removal from newly started processes.
func (c Client) applySessionEnvironment(ctx context.Context, name string, env map[string]string, scrub []string) error {
	for _, key := range scrub {
		if _, present := env[key]; present {
			continue
		}
		if _, err := c.run(ctx, []string{"set-environment", "-r", "-t", name, key}, nil); err != nil {
			return err
		}
	}
	return c.setSessionEnvironment(ctx, name, env)
}

func (c Client) setSessionEnvironment(ctx context.Context, name string, env map[string]string) error {
	for _, key := range sortedKeys(env) {
		if _, err := c.run(ctx, []string{"set-environment", "-t", name, key, env[key]}, nil); err != nil {
			return err
		}
	}
	return nil
}

// GlobalEnvironment returns the tmux server's global environment. This is the
// clean starting point for every session: it is what the user's own terminal
// sessions see, without direnv state that may be loaded in the pane that
// happens to launch wi.
func (c Client) GlobalEnvironment(ctx context.Context) (map[string]string, error) {
	return c.showEnvironment(ctx, "-g")
}

func (c Client) SessionEnvironment(ctx context.Context, name string) (map[string]string, error) {
	return c.showEnvironment(ctx, "-t", name)
}

func (c Client) SetSessionEnvironment(ctx context.Context, name string, environment map[string]string) error {
	return c.setSessionEnvironment(ctx, name, environment)
}

func (c Client) showEnvironment(ctx context.Context, args ...string) (map[string]string, error) {
	tmuxArgs := append([]string{"show-environment"}, args...)
	out, err := c.run(ctx, tmuxArgs, nil)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		env[key] = value
	}
	return env, nil
}

func (c Client) EnsureGatewaySession(ctx context.Context, spec SessionSpec, agentCommand, shellCommand []string) (bool, error) {
	created, err := c.EnsureCommandSession(ctx, spec, "agent", agentCommand)
	if err != nil {
		return false, err
	}
	defaultCommand, err := ShellCommand(shellCommand)
	if err != nil {
		return false, err
	}
	if err := c.setSessionOption(ctx, spec.Name, "default-command", defaultCommand); err != nil {
		return false, err
	}
	for _, window := range []struct {
		name    string
		command []string
	}{{"agent", agentCommand}, {"shell", shellCommand}} {
		if _, err := c.ensureWindowCommand(ctx, LaunchSpec{SessionName: spec.Name, WindowName: window.name, CWD: spec.CWD, Command: window.command, Env: spec.Env, Scrub: spec.Scrub}); err != nil {
			return false, err
		}
	}
	return created, nil
}

func (c Client) ensureWindowCommand(ctx context.Context, spec LaunchSpec) (bool, error) {
	name := strings.TrimSpace(spec.WindowName)
	if name == "" {
		return false, fmt.Errorf("window name is required")
	}
	exists, err := c.windowExists(ctx, spec.SessionName, name)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if len(spec.Env) > 0 {
		if err := c.applySessionEnvironment(ctx, spec.SessionName, spec.Env, spec.Scrub); err != nil {
			return false, err
		}
	}
	command, err := ShellCommand(spec.Command)
	if err != nil {
		return false, err
	}
	_, err = c.run(ctx, []string{"new-window", "-d", "-t", spec.SessionName + ":", "-n", name, "-c", spec.CWD, command}, nil)
	return err == nil, err
}

func (c Client) setSessionOption(ctx context.Context, session, name, value string) error {
	if strings.TrimSpace(session) == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("tmux session and option names are required")
	}
	_, err := c.run(ctx, []string{"set-option", "-t", session, name, value}, nil)
	return err
}

func (c Client) LaunchCommand(ctx context.Context, spec LaunchSpec) error {
	if strings.TrimSpace(spec.SessionName) == "" {
		return fmt.Errorf("tmux session name is required")
	}
	if len(spec.Command) == 0 {
		return fmt.Errorf("command is required")
	}
	if strings.TrimSpace(spec.CWD) == "" {
		return fmt.Errorf("command working directory is required")
	}
	if len(spec.Env) > 0 {
		if err := c.applySessionEnvironment(ctx, spec.SessionName, spec.Env, spec.Scrub); err != nil {
			return err
		}
	}
	command, err := ShellCommand(spec.Command)
	if err != nil {
		return err
	}
	if spec.ReuseAgentWindow {
		target := spec.SessionName + ":agent"
		exists, err := c.windowExists(ctx, spec.SessionName, "agent")
		if err != nil {
			return err
		}
		if exists {
			_, err = c.run(ctx, []string{"respawn-pane", "-k", "-c", spec.CWD, "-t", target, command}, nil)
			return err
		}
		_, err = c.run(ctx, []string{"new-window", "-t", spec.SessionName + ":", "-n", "agent", "-c", spec.CWD, command}, nil)
		return err
	}
	name := strings.TrimSpace(spec.WindowName)
	if name == "" {
		name = "agent"
	}
	_, err = c.run(ctx, []string{"new-window", "-t", spec.SessionName + ":", "-n", name, "-c", spec.CWD, command}, nil)
	return err
}

func ShellCommand(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("command is empty")
	}
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = shellQuote(arg)
	}
	return strings.Join(parts, " "), nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r))
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func (c Client) windowExists(ctx context.Context, session, name string) (bool, error) {
	out, err := c.run(ctx, []string{"list-windows", "-t", session, "-F", "#{window_name}"}, nil)
	if err != nil {
		return false, err
	}
	for _, window := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(window) == name {
			return true, nil
		}
	}
	return false, nil
}

func (c Client) AttachOrSwitch(ctx context.Context, name string, inTmux bool) error {
	return c.AttachOrSwitchClient(ctx, name, inTmux, "")
}

func (c Client) AttachOrSwitchClient(ctx context.Context, name string, inTmux bool, originatingClient string) error {
	if inTmux {
		args := []string{"switch-client"}
		client, err := c.resolveClient(ctx, originatingClient)
		if err != nil {
			return err
		}
		if client != "" {
			args = append(args, "-c", client)
		}
		// switch-client is a server command and does not need a controlling
		// terminal. Keeping it non-interactive allows tmux run-shell bindings to
		// navigate silently in the background.
		_, err = c.run(ctx, append(args, "-t", name), nil)
		return err
	}
	return c.runInteractive(ctx, []string{"attach-session", "-t", name}, nil)
}

func (c Client) resolveClient(ctx context.Context, originatingClient string) (string, error) {
	client := strings.TrimSpace(originatingClient)
	if strings.ContainsAny(client, "\r\n\x00") {
		return "", fmt.Errorf("invalid tmux client name")
	}
	// Treat an unexpanded tmux format from an older or misparsed binding as
	// absent rather than passing the literal to tmux.
	if strings.Contains(client, "#{") {
		client = ""
	}
	// Bindings capture the originating client before launching a popup or
	// background shell. Fall back to tmux discovery for manual invocations.
	if client == "" {
		if out, err := c.run(ctx, []string{"display-message", "-p", "#{client_name}"}, nil); err == nil {
			client = strings.TrimSpace(string(out))
		}
	}
	return client, nil
}

func (c Client) ListSessions(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, []string{"list-sessions", "-F", "#{session_name}"}, nil)
	if err != nil {
		message := strings.ToLower(err.Error())
		if isExitError(err) && (strings.Contains(message, "no server running") || strings.Contains(message, "no sessions") || strings.Contains(message, "no such file or directory")) {
			return []string{}, nil
		}
		return nil, err
	}
	sessions := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			sessions = append(sessions, name)
		}
	}
	sort.Strings(sessions)
	return sessions, nil
}

func (c Client) CopyMode(ctx context.Context, target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("tmux copy-mode target is required")
	}
	_, err := c.run(ctx, []string{"copy-mode", "-t", target}, nil)
	return err
}

func (c Client) KillSession(ctx context.Context, name string) error {
	_, err := c.run(ctx, []string{"kill-session", "-t", name}, nil)
	return err
}

func (c Client) SourceFile(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("tmux configuration path is required")
	}
	_, err := c.run(ctx, []string{"source-file", path}, nil)
	return err
}

func (c Client) KillServer(ctx context.Context) error {
	_, err := c.run(ctx, []string{"kill-server"}, nil)
	return err
}

func (c Client) GlobalOption(ctx context.Context, name string) (string, error) {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t\r\n\x00") {
		return "", fmt.Errorf("tmux option name is invalid")
	}
	out, err := c.run(ctx, []string{"show-options", "-gv", name}, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// KillSessionAsync asks the tmux server to destroy a session after a short
// delay. This lets a command running in that session persist its final durable
// state and exit before tmux tears down the command's own pane.
func (c Client) KillSessionAsync(ctx context.Context, name string) error {
	command := "sleep 0.2; tmux kill-session -t " + shellQuote(name)
	_, err := c.run(ctx, []string{"run-shell", "-b", command}, nil)
	return err
}

func (c Client) CurrentSession(ctx context.Context) (string, error) {
	out, err := c.run(ctx, []string{"display-message", "-p", "#S"}, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (c Client) PaneInfo(ctx context.Context, target string) (PaneInfo, error) {
	if strings.TrimSpace(target) == "" {
		return PaneInfo{}, fmt.Errorf("tmux target is required")
	}
	format := strings.Join([]string{"#{session_name}", "#{window_name}", "#{pane_id}", "#{pane_index}", "#{pane_pid}", "#{pane_current_command}", "#{pane_current_path}"}, "\t")
	out, err := c.run(ctx, []string{"display-message", "-p", "-t", target, format}, nil)
	if err != nil {
		return PaneInfo{}, err
	}
	return parsePaneInfo(strings.TrimRight(string(out), "\n"))
}

// ListPanes returns every pane from one tmux server call so observers do not
// fan out one subprocess per work item.
func (c Client) ListPanes(ctx context.Context) ([]PaneInfo, error) {
	format := strings.Join([]string{"#{session_name}", "#{window_name}", "#{pane_id}", "#{pane_index}", "#{pane_pid}", "#{pane_current_command}", "#{pane_current_path}"}, "\t")
	out, err := c.run(ctx, []string{"list-panes", "-a", "-F", format}, nil)
	if err != nil {
		return nil, err
	}
	panes := []PaneInfo{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		pane, err := parsePaneInfo(line)
		if err != nil {
			return nil, err
		}
		panes = append(panes, pane)
	}
	return panes, nil
}

func parsePaneInfo(line string) (PaneInfo, error) {
	parts := strings.SplitN(line, "\t", 7)
	if len(parts) != 7 {
		return PaneInfo{}, fmt.Errorf("unexpected tmux pane info output %q", strings.TrimSpace(line))
	}
	pid := 0
	if strings.TrimSpace(parts[4]) != "" {
		_, _ = fmt.Sscanf(parts[4], "%d", &pid)
	}
	return PaneInfo{SessionName: parts[0], WindowName: parts[1], PaneID: parts[2], PaneIndex: parts[3], PanePID: pid, Command: parts[5], CurrentPath: parts[6]}, nil
}

func (c Client) run(ctx context.Context, args []string, env []string) ([]byte, error) {
	path := c.Path
	if path == "" {
		path = "tmux"
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	args = c.serverArgs(args)
	out, err := runner.Run(ctx, path, args, env)
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text != "" {
			return nil, fmt.Errorf("tmux %s: %s: %w", strings.Join(args, " "), text, err)
		}
		return nil, fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func (c Client) runInteractive(ctx context.Context, args []string, env []string) error {
	path := c.Path
	if path == "" {
		path = "tmux"
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	args = c.serverArgs(args)
	if interactive, ok := runner.(InteractiveRunner); ok {
		if err := interactive.RunInteractive(ctx, path, args, env); err != nil {
			return fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	// run already adds server arguments; avoid adding them twice when the
	// configured runner does not implement interactive execution.
	plain := c
	plain.SocketPath, plain.ConfigPath = "", ""
	_, err := plain.run(ctx, args, env)
	return err
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, replace := extra[key]; replace {
			continue
		}
		seen[key] = true
		out = append(out, kv)
	}
	for _, key := range sortedKeys(extra) {
		if seen[key] {
			continue
		}
		out = append(out, key+"="+extra[key])
	}
	return out
}

func scrubSet(scrub []string) map[string]bool {
	set := make(map[string]bool, len(scrub))
	for _, key := range scrub {
		set[key] = true
	}
	return set
}

func envWithoutKeys(entries []string, scrub map[string]bool) []string {
	filtered := make([]string, 0, len(entries))
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || scrub[key] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func isExitError(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return true
	}
	return strings.Contains(err.Error(), "exit status")
}
