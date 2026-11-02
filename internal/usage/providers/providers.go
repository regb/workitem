// Package providers discovers native subscription sources independently of the
// execution harness. Pi credentials are never inspected or used for discovery.
package providers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/usage"
	"github.com/regb/workitem/internal/usage/claude"
	"github.com/regb/workitem/internal/usage/codex"
	"github.com/regb/workitem/internal/usage/opencode"
)

func CacheRoot(env map[string]string) (string, error) {
	path, err := root(env, "XDG_CACHE_HOME", ".cache")
	if err != nil {
		return "", err
	}
	return filepath.Join(path, "wi", "quota-v5"), nil
}

func root(env map[string]string, key, dir string) (string, error) {
	home := env["HOME"]
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("absolute HOME is required for usage inspection")
	}
	v := env[key]
	if v == "" {
		v = filepath.Join(home, dir)
	}
	if !filepath.IsAbs(v) {
		return "", fmt.Errorf("%s must be an absolute path", key)
	}
	return v, nil
}

// Only use the caller's absolute PATH entries, never a checkout-local executable.
func executable(env map[string]string, name string) string {
	for _, dir := range filepath.SplitList(env["PATH"]) {
		if !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path
		}
	}
	return ""
}

// Discover includes installed sources by default. An explicit provider retains
// missing clients as unavailable results. Unselected clients' configuration is
// never validated or loaded.
func Discover(env map[string]string, requested string) ([]usage.Provider, error) {
	result := []usage.Provider{}
	for _, name := range []string{"claude", "codex", "opencode-go"} {
		if requested != "" && requested != name {
			continue
		}
		client := name
		if client == "opencode-go" {
			client = "opencode"
		}
		path := executable(env, client)
		if requested == "" && path == "" {
			continue
		}
		p, err := adapter(env, name, []string{path})
		if err != nil {
			return nil, err
		}
		if path == "" {
			p = missingClient{account: p.Account(), client: client}
		}
		result = append(result, p)
	}
	return result, nil
}

func adapter(env map[string]string, name string, command []string) (usage.Provider, error) {
	switch name {
	case "claude":
		path, err := root(env, "CLAUDE_CONFIG_DIR", ".claude")
		if err != nil {
			return nil, err
		}
		return &claude.Adapter{Root: path, Command: command, Environment: env}, nil
	case "codex":
		path, err := root(env, "CODEX_HOME", ".codex")
		if err != nil {
			return nil, err
		}
		return &codex.Adapter{Root: path, Command: command, Environment: env}, nil
	default:
		data, err := root(env, "XDG_DATA_HOME", ".local/share")
		if err != nil {
			return nil, err
		}
		config := env["OPENCODE_CONFIG_DIR"]
		if config == "" {
			path, err := root(env, "XDG_CONFIG_HOME", ".config")
			if err != nil {
				return nil, err
			}
			config = filepath.Join(path, "opencode")
		}
		if !filepath.IsAbs(config) {
			return nil, fmt.Errorf("OPENCODE_CONFIG_DIR must be an absolute path")
		}
		return &opencode.Adapter{Root: filepath.Join(data, "opencode") + "\x00" + config, Command: command, Environment: env}, nil
	}
}

type missingClient struct {
	account usage.Account
	client  string
}

func (m missingClient) Account() usage.Account { return m.account }
func (m missingClient) Quota(context.Context) usage.Quota {
	return usage.Quota{Account: m.account, State: usage.Unavailable, CheckedAt: time.Now().UTC(), Windows: []usage.Window{},
		Detail: "Native " + m.client + " CLI is not installed on PATH. Install and sign in with that native client to track its subscription."}
}

func CanonicalProvider(value string) string {
	switch strings.ToLower(value) {
	case "claude", "anthropic":
		return "claude"
	case "codex", "openai":
		return "codex"
	case "opencode", "opencode-go":
		return "opencode-go"
	}
	return value
}
