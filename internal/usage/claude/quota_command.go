package claude

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/usage"
)

func (a *Adapter) nativeQuota(ctx context.Context) usage.Quota {
	q := usage.Quota{Account: a.Account(), CheckedAt: a.now(), State: usage.Unavailable, Windows: []usage.Window{}}
	fail := func(detail string) usage.Quota {
		q.Detail = detail
		return q
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	env := map[string]string{}
	for k, v := range a.Environment {
		env[k] = v
	}
	// Preserve an unset default CLAUDE_CONFIG_DIR: explicitly setting it can
	// select a different native Keychain service. Keep explicit literal roots.
	if env["CLAUDE_CONFIG_DIR"] == "" && filepath.Clean(a.Root) != filepath.Join(env["HOME"], ".claude") {
		env["CLAUDE_CONFIG_DIR"] = a.Root
	}
	// Do not force DISABLE_NONESSENTIAL_TRAFFIC: Claude classifies quota
	// metadata as nonessential traffic. Preserve any explicit caller setting.
	env["DISABLE_AUTOUPDATER"] = "1"
	env["NO_COLOR"] = "1"
	delete(env, "CLAUDECODE")
	delete(env, "CLAUDE_CODE_SESSION_ID")
	delete(env, "CLAUDE_CODE_PROJECT_DIR_NAME")
	base := []string{"--setting-sources", "", "--settings", `{"disableAllHooks":true}`}
	authArgs := append(append([]string{}, base...), "auth", "status")
	authRaw, err := usage.RunNativeCommand(ctx, a.Command, authArgs, env)
	if err != nil {
		return fail("Native Claude authentication status is unavailable. Sign in or update Claude Code.")
	}
	var auth struct {
		LoggedIn bool   `json:"loggedIn"`
		Method   string `json:"authMethod"`
		Plan     string `json:"subscriptionType"`
	}
	if json.Unmarshal(authRaw, &auth) != nil || !auth.LoggedIn {
		return fail("Native Claude reports no usable login. Sign in with Claude Code.")
	}
	if auth.Method != "claude.ai" && auth.Method != "oauth_token" {
		q.State = usage.Unsupported
		q.Detail = "Selected native Claude authentication does not expose a Claude subscription."
		return q
	}
	args := append(append([]string{}, base...), "-p", "/usage", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--tools", "", "--max-turns", "0", "--max-budget-usd", "0.000001")
	raw, err := usage.RunNativeCommand(ctx, a.Command, args, env)
	if err != nil {
		return fail("Native Claude local usage command failed or timed out; no fallback prompt was sent.")
	}
	windows, err := decodeLocalUsage(raw)
	if err != nil {
		if errors.Is(err, errNoNativeQuota) {
			if flag := env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"]; flag == "1" || strings.EqualFold(flag, "true") {
				return fail("Native Claude returned no plan limits while CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC is enabled. Remaining quota is unknown; wi respects this native traffic restriction.")
			}
			if auth.Plan == "team" {
				return fail("Native Claude recognizes the Team login but returned no plan limits. Remaining quota is unknown, not exhausted. Check /usage in Claude Code.")
			}
			return fail("Native Claude returned no plan limits. Remaining quota is unknown, not exhausted. Check /usage in Claude Code.")
		}
		return fail("Claude did not return a recognized zero-turn quota report. Update Claude Code; no alternate prompt or direct endpoint was attempted.")
	}
	q.Windows = windows
	now := a.now()
	q.State = usage.OK
	q.ObservedAt = &now
	return usage.AgeObservation(q, now)
}
