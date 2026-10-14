# Optional Claude native tests

`scripts/test_claude_native.py` runs real Wi items against the installed Claude CLI. It is separate from the normal Go suite. Native cases skip by default; a request-free runner unit test still runs:

```sh
python3 scripts/test_claude_native.py -v
```

Opt in explicitly. These tests make model requests using your account and run a harmless `sleep` tool:

```sh
WI_TEST_CLAUDE_NATIVE=1 python3 scripts/test_claude_native.py -v
```

The runner skips when Claude or Git is missing, when Claude is older than 2.1.299, or when no supported authentication source is available. Only the mode-switching test requires tmux; it skips if tmux is missing. Go is required to build Wi unless you provide `WI_TEST_WI_BINARY`.

Optional settings:

- `WI_TEST_CLAUDE_EXECUTABLE`: executable path or command name, default `claude`.
- `WI_TEST_WI_BINARY`: absolute path to a previously built Wi binary. Otherwise the runner builds the current source into a temporary directory.
- `WI_TEST_CLAUDE_CONFIG_DIR`: directory containing `.credentials.json`. Defaults to `CLAUDE_CONFIG_DIR`, then `~/.claude`.
- `WI_TEST_CLAUDE_MODEL`: model name, default `haiku`.
- `ANTHROPIC_API_KEY`: optional alternative to a credential file. API-key requests bill separately from subscription CLI login.

Run only the mode-switching case:

```sh
WI_TEST_CLAUDE_NATIVE=1 python3 scripts/test_claude_native.py ClaudeNativeTests.test_mode_switching -v
```

## Coverage

The headless case verifies successive turns, context recall, same-conversation restart, PNG vision input, multiple API-delta snapshots before settlement, tool execution, interrupt receipts, cancellation of queued steering/follow-up input, and a new turn after abort. It also checks that headless creates no tmux server.

The mode-switching case starts TUI, remembers a unique value, stops it, starts headless against the same conversation, recalls the value, remembers another value, and switches back to TUI. The final TUI must recall both values. Each transition gets a new runtime ID without changing the conversation ID. Startup waits for both input readiness and bootstrap capabilities; those observations can arrive separately. Ordinary TUI settlement currently omits command IDs, so the test correlates settlement with the confirmed input's runtime ID and timestamp. Headless uses exact command IDs.

## Isolation and cleanup

Each test creates a tiny synthetic Git repository and registers it in private HOME/XDG roots under `/tmp/wcn-*`. It does not onboard an operator repository, use the operator's daemon, or reuse an existing tmux server. The tmux socket directory belongs only to that fixture.

The fixture preconfigures onboarding, permission-bypass acceptance, and trust for its synthetic checkout in its private Claude settings before launch. It never answers live authentication, trust, or setup dialogs. Unexpected dialogs cause readiness or delivery failure rather than automated keystrokes.

For subscription login, the runner copies only the existing credential file into its private Claude configuration with mode 0600. This is test setup, not product authentication code. It neither copies host hooks/settings nor prints credentials or the environment. Cleanup removes the copied credential file even after failures, requests shutdown for all items in the fixture, waits for their runtimes to be offline, stops only the fixture's tmux server, and stops its coordinator.

Private native transcripts, runtime logs, compact events, and the last private content snapshot remain in the printed evidence directory for diagnosis. Do not publish them wholesale. The temporary Wi build is removed after the tests. No tests run implicitly just because Claude is installed.
