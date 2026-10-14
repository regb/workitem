---
title: wi documentation
description: Capture, delegate, review, and finish short-lived development tasks.
---

`wi` is a Linux-first CLI for bounded, AI-assisted development tasks. Create an item with a clear objective, supervise its agent, review the result, and archive it when done. Use another item for a different objective.

The active task is short-lived; its record is durable. Descriptions, branches, conversations, and history survive restarts and remain after archiving. Worktrees, runtimes, and terminals are replaceable resources.

:::note[Start here]
Follow [Installation](getting-started/installation/) and the [Quick start](getting-started/quick-start/) for interactive and headless workflows.
:::

## A common workflow

```bash
wi init
wi repo add project --from /path/to/repository --branch main
wi new --title "Add request retries" --repo project --desc-file task.md --start
wi list
wi switch add-request-retries
```

After reviewing, testing, and committing the changes:

```bash
wi merge --item add-request-retries
wi archive add-request-retries
```

`new --start` sends the configured startup message. Existing-item `start` and `resume` open a runtime without sending one. Descriptions and prompts are separate; see [Delegate work](guides/delegation/).

## Find the right guide

- [Work items](concepts/work-items/) and [Lifecycle](concepts/lifecycle/) explain task scope and state.
- [Repositories](concepts/repositories/), [Workspaces](concepts/workspaces/), and [Agents](concepts/agents/) explain the attached resources.
- [Everyday workflow](guides/everyday-workflow/) covers capture through cleanup.
- [Review agent work](guides/review-and-follow-up/) covers verification and corrections.
- [Use tmux](guides/tmux/) covers interactive navigation.
- [Command map](reference/commands/), [Configuration](reference/configuration/), and [Troubleshooting](reference/troubleshooting/) cover operation and diagnosis.
