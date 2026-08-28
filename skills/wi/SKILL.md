---
name: wi
description: Use wi from an active agent session to read task context, file durable follow-up work, review items, or make explicitly requested updates.
---

# wi Agent Recipes

Use this skill for operations that make sense from an already running agent. It is not a general interactive workflow guide.

## Rules

- Use `wi` commands; never edit wi manifests, descriptions, events, runtime files, locks, or Pi sessions directly.
- IDs are durable identity; slugs are temporary aliases.
- Do not contact another agent or change item metadata unless requested.
- Do not perform navigation, workspace, terminal, or workflow cleanup operations through this skill.
- Prefer `--item <selector>` when acting on an item other than the current one.

## Read task context

```bash
wi show --json
wi show --json --item <selector>
```

`show` returns the manifest and the hydrated `DESCRIPTION.md` content in `.description`. Use the title and description as the task specification. If no current item resolves, continue from repository/conversation context when possible.

## File follow-up work

Use this when planning or implementation reveals independently actionable work. The future session may not have this conversation, so the title and description must be a self-contained handoff.

### Recipe

Include all context needed by a future agent. When running in Agentbox, use the sandbox-installed `wi` command and its restricted daemon endpoint.

1. Read the source item with `wi show --json`.
2. Choose a title that names the subsystem and concrete outcome.
3. Write only the context needed by the future agent: why, evidence, constraints, next step, and definition of done.
4. Create the backlog item with `wi new`.
5. Report the returned ID, slug, and title.

```bash
handoff=$(mktemp)
cat >"$handoff" <<'EOF'
# Context
Source item: <ID and title, if any>
<Why this work was discovered and why it is separate.>

# Relevant details
- <Observed behavior, exact error, or reproduction command>
- <Relevant files, functions, commits, links, or current implementation state>
- <Decisions, constraints, dependencies, and important non-goals>

# Suggested next step
<Useful starting point for a new agent.>

# Done when
- <Observable outcome>
- <Required tests or validation>
EOF

wi new --json --label follow-up --desc-file "$handoff" \
  "<clear outcome-oriented title>"
rm -f "$handoff"
```

Adapt the headings and omit empty sections. Capture facts and decisions, not a transcript dump. Never write “see current conversation” as the handoff.

Good title: `Harden runtime shutdown against stale PID reuse`

Bad title: `Agent issue follow-up`

### Useful creation variants

```bash
wi new --json --deep --desc-file "$handoff" "<title>"             # focused deep work
wi new --json --repo /path/to/repo --desc-file "$handoff" "<title>"
wi new --json --base HEAD --desc-file "$handoff" "<title>"        # intentionally depends on current branch
wi new --json --home --desc-file "$handoff" "<title>"             # explicitly requested trunk/home work
wi new --json --label backend --label bug --desc-file "$handoff" "<title>"
```

Normally omit `--base`: the item uses the local default branch commit without fetching. Use `--base HEAD` only for intentionally stacked work. Use `--home` only when the user explicitly wants the repository's primary default-branch checkout.

Ordinary `wi new` creates a `backlog` item; it does not start work. For several follow-ups, create one handoff at a time and verify each result before continuing.

To delegate immediately without switching away from the current session, pass the self-contained description directly as `--prompt`:

```bash
wi new --json --prompt "<complete task context and acceptance criteria>" "<title>"
```

This persists the prompt as the description, moves the item to `working`, starts the normal tmux/TUI runtime without attaching or switching this session, and sends the prompt to Pi. The user can attach later as usual. Add `--agent-mode rpc` only for intentionally headless work. Use ordinary `--desc-file` to file backlog work.

The restricted agent endpoint rejects `--prompt`, lifecycle changes, runtime control, and other operator actions. When that endpoint is active, file backlog work with `--desc-file` instead of trying to bypass the restriction.

## Review work items

Use when asked to inventory, review, or triage current work:

```bash
wi list --json                         # active items plus live observations
wi list --json --no-agent              # durable-state-only inventory
wi list --json --state working
wi list --json --label +backend --label -blocked
wi list --json --all                   # include archived
```

The list is a projection, not the full task specification. Inspect relevant items with:

```bash
wi show --json --item <selector>
```

Report lifecycle state separately from derived agent/worktree status. Do not mutate items merely because they appear stale.

## Contact another running agent

Only when explicitly asked, and only when the available daemon endpoint permits runtime control:

```bash
wi agent runtime status --json --item <selector>
wi agent control send --json --item <selector> "<self-contained message>"
wi agent monitor --json --item <selector> --limit 50
```

For a substantial message, use `wi agent control send --file <path>`. Add `--follow-up` when the input must wait for the current run to settle. To wait for new runtime events:

```bash
wi agent monitor --json --item <selector> --follow
```

Bound unattended follow mode with an external timeout. Delivery is not completion. Runtime events are compact, so use the target item's conversation when full response text is required. Do not bypass a restricted agent endpoint that rejects runtime control.

## Update labels or state

Only when requested:

```bash
wi label --json --item <selector>                         # inspect
wi label --item <selector> backend reliability           # add
wi label --remove --item <selector> blocked               # remove

wi state show --json --item <selector>
wi state set waiting --item <selector>
wi state set working --item <selector>
wi state set backlog --item <selector>
wi state set archived --item <selector>
```

`state set` changes durable lifecycle only. It does not start/stop agents or clean terminal/workspace resources; do not present it as workflow cleanup.

## Report outcomes

Report only verified results: created IDs, slugs, titles, control delivery, observed runtime evidence, or metadata actually changed. Do not imply that creating an item started work or that delivering a message completed it.
