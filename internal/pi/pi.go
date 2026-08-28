package pi

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/agent"
)

//go:embed bridge.ts
var bridgeSource []byte

type Runner interface {
	RunInteractive(ctx context.Context, path string, args []string, cwd string, env []string) error
}

type HeadlessRunner interface {
	RunHeadless(ctx context.Context, path string, args []string, env []string, spec ExecSpec) error
}

type ExecRunner struct{}

func (ExecRunner) RunInteractive(ctx context.Context, path string, args []string, cwd string, env []string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = cwd
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (ExecRunner) RunHeadless(ctx context.Context, path string, args []string, env []string, spec ExecSpec) error {
	return runNativeRPC(ctx, path, args, env, spec)
}

type Client struct {
	Path   string
	Runner Runner
}

type ExecSpec struct {
	SessionPath       string
	BridgePath        string
	Mode              agent.Mode
	CWD               string
	Env               map[string]string
	LogPath           string
	WorkItemID        string
	RuntimeID         string
	ControlSocketPath string
	DaemonSocketPath  string
}

func New(path string) Client {
	if path == "" {
		path = "pi"
	}
	return Client{Path: path, Runner: ExecRunner{}}
}

func Args(sessionPath string) ([]string, error) {
	return ArgsForMode(sessionPath, "", agent.ModeTUI)
}

func ArgsForMode(sessionPath, bridgePath string, mode agent.Mode) ([]string, error) {
	runtime, err := runtimeModeFor(mode)
	if err != nil {
		return nil, err
	}
	return runtime.Args(strings.TrimSpace(sessionPath), strings.TrimSpace(bridgePath))
}

func BridgePath() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache directory for Pi bridge: %w", err)
	}
	dir := filepath.Join(cacheDir, "wi", "pi")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create Pi bridge cache: %w", err)
	}
	path := filepath.Join(dir, "wi-bridge.ts")
	if current, err := os.ReadFile(path); err == nil && string(current) == string(bridgeSource) {
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, ".wi-bridge-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(bridgeSource); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func (c Client) Exec(ctx context.Context, sessionPath, cwd string, env map[string]string) error {
	return c.ExecMode(ctx, ExecSpec{SessionPath: sessionPath, Mode: agent.ModeTUI, CWD: cwd, Env: env})
}

func (c Client) ExecMode(ctx context.Context, spec ExecSpec) error {
	bridgePath := spec.BridgePath
	if spec.Mode == agent.ModeTUI && bridgePath == "" {
		var err error
		bridgePath, err = BridgePath()
		if err != nil {
			return err
		}
	}
	runtime, err := runtimeModeFor(spec.Mode)
	if err != nil {
		return err
	}
	args, err := runtime.Args(strings.TrimSpace(spec.SessionPath), strings.TrimSpace(bridgePath))
	if err != nil {
		return err
	}
	path := c.Path
	if path == "" {
		path = "pi"
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	mergedEnv := os.Environ()
	if len(spec.Env) > 0 {
		mergedEnv = mergeEnv(mergedEnv, spec.Env)
	}
	if err := runtime.Run(ctx, runner, path, args, spec, mergedEnv); err != nil {
		return fmt.Errorf("pi %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func mergeEnv(base []string, extra map[string]string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if value, replace := extra[key]; replace {
			out = append(out, key+"="+value)
			seen[key] = true
			continue
		}
		seen[key] = true
		out = append(out, kv)
	}
	for key, value := range extra {
		if !seen[key] {
			out = append(out, key+"="+value)
		}
	}
	return out
}
