---
title: Remaining subscription quota
description: Show remaining Claude, Codex, and OpenCode Go quota in text or JSON.
---

```bash
wi usage
wi usage --json
wi usage --provider opencode-go --refresh
```

`wi usage` shows how much of each subscription's quota is left: Claude, OpenAI through Codex, and OpenCode Go, which Pi can use. It reports subscriptions, not agent profiles: Pi is a harness without a subscription of its own, and one Pi profile may use several accounts. Each subscription is read through its own native client, which keeps its login. It does not track tokens, sessions, history, or costs, and never sends model prompts. No repository, execution driver, or tmux is involved. Pi credentials are not inspected or reused.

It is a bundled plugin, installed with wi as `wi-usage`. Enable it in Wi's configuration file:

```toml
[plugins.usage]
enabled = true
```

`wi usage` then runs it; `wi usage show` is the same. The web Settings → Usage page shows the same results, with a Refresh button. Usage is not part of the [daemon API](../api/): it reads the native clients installed for your user, so `wi usage` runs the plugin itself; the daemon does not run it.

## Text summary

Default output groups quota by provider and period:

```text
Remaining subscription quota.
Claude
  5-hour  [███████░░░]      76% left  resets in 1h 9m
  Weekly  [██████░░░░]      60% left  resets in 4d 11h

OpenCode Go
  5-hour   [█████████░]   98.37% left  resets in 1h 33m
  Weekly   [█████████░]   96.76% left  resets in 11h 29m
  Monthly  [█████████░]    95.5% left  resets in 12d 23h
```

Filled bars mean **remaining** quota. Segments round down; percentages keep up to two decimal places without rounding small positive values to exhausted or full quota. Unknown values have no bar. Missing resets say `reset unknown`. Countdown values use the current clock and stored reset time.

Model/surface scopes and distinct native buckets retain their labels. Multiple accounts under one provider display their IDs. Stale results include an observation-age warning; errors include a short explanation.

## Commands and sources

| Option | Meaning |
| --- | --- |
| `--json` | Return the versioned JSON envelope below |
| `--provider NAME` | Select `anthropic`/`claude`, `openai`/`codex`, or `opencode-go`/`opencode` |
| `--account ID` | Select an account/configuration ID from JSON output |
| `--refresh` | Request a native refresh, subject to cache cooldown/backoff |

Default discovery includes only executables on absolute PATH entries. An explicit query for a missing client returns an unavailable result. Configuration for unselected clients is ignored. There is no built-in polling, status-line collector, or background task; call `wi usage --json` from your own scheduler when needed.

## JSON for automation

The schema is the same for every source:

```json
{
  "schema_version": 1,
  "results": [
    {
      "provider": "codex",
      "account": "codex:opaque-slot",
      "observed_at": "2026-10-23T17:17:08Z",
      "windows": [
        {
          "id": "codex/primary",
          "remaining_percent": 71,
          "resets_at": "2026-10-23T22:17:05Z",
          "duration_seconds": 18000
        }
      ],
      "included_usage_allowed": true
    }
  ]
}
```

- `provider` is `claude`, `codex`, or `opencode-go`, regardless of the input alias.
- `account` is an opaque selectable binding, not a billing identifier. It is omitted when unknown.
- `observed_at` is when wi received the observation, not a provider-certified timestamp. Unknown observation time is `null`.
- `windows` and `results` are always arrays. Missing windows mean unknown data, never unlimited capacity.
- `remaining_percent` is 0–100 or `null`. Zero means an observed exhausted allowance. It is not an absolute token capacity.
- `resets_at` is an RFC 3339 timestamp or `null`. Unknown `duration_seconds` is also `null`.
- Optional window `scope` contains native `model` and/or `surface` labels.
- Optional `included_usage_allowed` preserves a native permission flag. An explicit `false` can block included usage even when some percentages are positive. An omitted flag is unknown; wi never infers permission from percentages.
- Optional `credits` contains `remaining`, `unlimited`, and `unit`. These are provider credit units, not token counts or estimated dollar costs.

Successful results have no status/error fields. A failed or unreliable observation includes:

```json
"error": {
  "code": "unavailable",
  "message": "Native quota query failed or timed out."
}
```

Error codes are `unsupported`, `unavailable`, `expired_auth`, and `stale`. A `stale` error accompanies `stale: true` and may retain the previous observation and quota. Other unavailable results have an empty `windows` array and no observation. Expired windows are removed rather than rolled forward as zero or full allowance. Always consider observation age and errors before automating decisions.

```bash
wi usage --json | jq '.results[] | {provider, observed_at, windows, stale, error}'
```

Provider errors return exit code 0 so one missing source does not hide another. Invalid options or unmatched account selection return 2; local configuration/cache failures return 1. Inspect per-result errors, not exit status alone.

## Native authentication and safety

**Claude Code** uses native `auth status`, followed by its local `/usage` command. Wi accepts the structured `usage_report.rate_limits` meters only after verifying the final result reports the local usage command, zero model turns, zero model API time, and zero cost. Human-readable screen text, activity recommendations, and session counters are not parsed. Use a current Claude Code version with structured usage reports; the integration was verified with v2.1.308. Native null/missing meters remain unknown, including for Team logins.

The query uses a temporary directory, disables hooks/tools/MCP and session persistence, and applies zero-turn guards. Authentication stays inside Claude Code, including its Keychain access. Explicit `CLAUDE_CONFIG_DIR` values are preserved; an unset default remains unset. Wi respects `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, which can suppress native quota metadata. See the [SDKUsageReport reference](https://code.claude.com/docs/en/agent-sdk/typescript#sdkusagereport).

**Codex** uses its documented `app-server --listen stdio://` protocol, verified with v0.160.0. Wi initializes the protocol, calls `account/read` with `refreshToken: false`, and reads `account/rateLimits/read`. No thread, turn, login operation, or reset-credit consumption is requested. Only the selected ChatGPT subscription login is used; other saved logins are not substituted. Native errors are sanitized. Different quota buckets are not double-counted. See the [app-server reference](https://developers.openai.com/codex/app-server).

**OpenCode Go** uses a bundled server plugin loaded under a private temporary directory with `opencode api --standalone`. No plugin is permanently installed and no existing service or configuration is changed. The plugin uses OpenCode's selected Console OAuth connection and workspace; API keys, cookies, and other saved accounts are not substituted. Go Plus is supported too. Caller configuration and XDG roots are preserved. Configuration disabling local/project plugins can prevent the bridge from loading; wi reports this rather than overriding it.

Inside OpenCode, the plugin reads `/console/api/orgs/current` and `/console/api/go/status`, verifies the workspace and subscription owner, and rechecks the selected connection before returning. Credentials, raw monetary counters, and raw HTTP errors never leave OpenCode. Percentages use server-supplied limits, not hardcoded dollar allowances. Redirects and unofficial Console origins are rejected. Identity/entitlement failures discard old data instead of retaining it as stale. No billing setting or extra-usage switch is changed.

This bridge was verified with OpenCode v2.1.4. The Console paths are observed frontend interfaces, not a documented stable quota API. Schema changes fail closed. It reports the selected account's Go subscription, not necessarily the funding of every OpenCode model request.

All native queries run outside the operator's checkout, with bounded subprocess output and lifetime. The native clients own credential renewal; wi never exports OAuth or opens their credential files.

## Caching and identity

Only the latest normalized quota per account/configuration binding is cached, under `$XDG_CACHE_HOME/wi/quota-v5` or `~/.cache/wi/quota-v5`, with private permissions. No usage history or coordinator events are recorded. Earlier formats are ignored. Concurrent queries share a file lock; malformed or interrupted cache writes are rebuildable.

Normal reads reuse observations for five minutes. `--refresh` has a one-minute minimum interval. Failures back off for at least five minutes, including explicit refresh; native rate-limit hints can extend this. Wi does not retry or fall back to HTTP outside the native client.

Claude/Codex account IDs identify `$CLAUDE_CONFIG_DIR` or `~/.claude`, and `$CODEX_HOME` or `~/.codex`. These are configuration slots, not verified billing identities. A login change can leave the previous observation visible until cache expiry; observation time does not prove the current login's quota.

OpenCode Go's ID also includes the native connection/workspace binding. Native selection is resolved before cache lookup/account filtering and rechecked before returning cached data. Selecting a different account/workspace cannot reuse the previous binding's quota. Only rechecked matching identities may retain stale data after a transient failure.
