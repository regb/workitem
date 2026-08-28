package direnv

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/model"
)

type Status = model.DirenvStatus

type Runner interface {
	Run(ctx context.Context, path string, args []string, dir string, env []string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, path string, args []string, dir string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			out = append(out, exitErr.Stderr...)
		}
	}
	return out, err
}

type Client struct {
	Path   string
	Runner Runner
}

func New(path string) Client {
	if path == "" {
		path = "direnv"
	}
	return Client{Path: path, Runner: ExecRunner{}}
}

func (c Client) Status(ctx context.Context, dir string) (Status, error) {
	if strings.TrimSpace(dir) == "" {
		return Status{}, fmt.Errorf("directory is required")
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	out, err := runner.Run(ctx, c.Path, []string{"status"}, dir, nil)
	if err != nil {
		return Status{Raw: string(out)}, fmt.Errorf("direnv status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return ParseStatus(string(out)), nil
}

func (c Client) Environment(ctx context.Context, dir string, base map[string]string) (map[string]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("directory is required")
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	// `direnv export json` is only a delta from the caller's current
	// environment, so values already loaded by the invoking shell are omitted
	// and inherited pollution from other direnv directories is never removed.
	// Execute env inside a fresh `direnv exec` instead: direnv re-evaluates the
	// worktree .envrc against the supplied base environment, giving callers the
	// complete target environment with fresh sourced values and without state
	// inherited from wherever wi itself was launched.
	if len(base) == 0 {
		base = envFromSlice(os.Environ())
	}
	envSlice := make([]string, 0, len(base))
	for key, value := range base {
		envSlice = append(envSlice, key+"="+value)
	}
	out, err := runner.Run(ctx, c.Path, []string{"exec", dir, "env", "-0"}, dir, envSlice)
	if err != nil {
		return nil, fmt.Errorf("direnv exec environment: %w: %s", err, strings.TrimSpace(string(out)))
	}
	env := map[string]string{}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("parse direnv environment: malformed entry")
		}
		env[key] = value
	}
	return env, nil
}

func (c Client) Allow(ctx context.Context, rcPath string) error {
	return c.setTrust(ctx, "allow", rcPath)
}

func (c Client) Deny(ctx context.Context, rcPath string) error {
	return c.setTrust(ctx, "deny", rcPath)
}

func (c Client) setTrust(ctx context.Context, action, rcPath string) error {
	if strings.TrimSpace(rcPath) == "" {
		return fmt.Errorf(".envrc path is required")
	}
	abs, err := filepath.Abs(rcPath)
	if err != nil {
		return err
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	out, err := runner.Run(ctx, c.Path, []string{action, abs}, filepath.Dir(abs), nil)
	if err != nil {
		return fmt.Errorf("direnv %s: %w: %s", action, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ManagedVariables decodes direnv's compressed DIRENV_DIFF state and returns the
// set of variable names it manages (both exported and unexported names combined).
// A long-lived tmux server captures the environment of whatever shell started it;
// that environment can contain project secrets from another worktree's .envrc. The
// decoded names let wi scrub exactly those variables from the base environment and
// from every session it creates, instead of propagating unrelated secrets.
func ManagedVariables(direnvDiff string) map[string]bool {
	vars := map[string]bool{}
	if strings.TrimSpace(direnvDiff) == "" {
		return vars
	}
	raw, err := base64.URLEncoding.DecodeString(direnvDiff)
	if err != nil {
		return vars
	}
	reader, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return vars
	}
	data, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		return vars
	}
	var state struct {
		Put   map[string]json.RawMessage `json:"p"`
		Set   map[string]json.RawMessage `json:"s"`
		Unset map[string]json.RawMessage `json:"n"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return vars
	}
	for _, group := range []map[string]json.RawMessage{state.Put, state.Set, state.Unset} {
		for key := range group {
			vars[key] = true
		}
	}
	return vars
}

func envFromSlice(entries []string) map[string]string {
	env := map[string]string{}
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	return env
}

func ParseStatus(out string) Status {
	st := Status{Raw: out}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Found RC path "):
			st.Found = true
			st.RCPath = strings.TrimSpace(strings.TrimPrefix(line, "Found RC path "))
		case strings.HasPrefix(line, "Found RC allowed "):
			value := strings.TrimSpace(strings.TrimPrefix(line, "Found RC allowed "))
			// direnv 2.37 reports its internal RC status enum: 0 means
			// allowed and 1 means denied. Older versions emitted booleans.
			st.Allowed = value == "true" || value == "0"
		}
	}
	return st
}
