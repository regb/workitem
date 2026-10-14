---
title: Attention
description: Understand how wi identifies work that needs a human decision.
---

Attention answers a practical question: which active item should you inspect next?

An item normally needs attention when it is `working`, its agent is idle, and neither agent nor worktree observation reports a problem. Busy work appears in progress. Waiting work remains outside the attention queue until resumed.

An intermediate assistant or tool error does not end an active agent loop. Pi's hooks keep the item busy through retries, recovery, compaction, and queued work. Only `agent_settled` ends that loop: successful settlement is idle, and failed settlement is a problem. Current-owner hooks take priority over intermediate native messages while the verified runtime is online. Offline or fatal runtimes remain problems. There is no error-recovery timeout, and live active loops are exempt from native-history staleness checks. These observations do not change the item's lifecycle.

Older runtimes without live hooks still use native-history inference. Installing Wi does not inject hooks into a running Pi process; explicitly restart that item’s agent to load the updated driver.

## Inspect the queue

```bash
wi attention queue
wi attention activity auth
wi list
```

The activity view shows the latest prompt, completion, and defer timestamps used to rank the queue. The `attention.priority` setting selects the ranking strategy; see [Configuration](../../reference/configuration/#attention-ranking).

## Move through ready work

```bash
wi next
```

`next` selects from the ranked attention queue and enters the selected item's TUI. It wraps when it reaches the end.

Common variants combine an explicit action on the current item with selection of the next one:

```bash
wi next --defer   # move the current ready item behind the others
wi next --wait    # mark the current working item waiting
wi next --archive # archive safely, then enter a distinct successor
```

These actions preserve successful partial outcomes. For example, a recorded defer remains even if entering the next tmux session fails.

## Use the queue from scripts

```bash
wi --json attention queue
wi --json next
```

JSON mode returns the same ranked queue used by interactive commands. It may select and prepare an item, but it never attaches a terminal.

## Filter the working set

Attention uses the same label rules as `wi list`:

```bash
wi next --label +backend --label -blocked
```

Rules can come from configuration, `WI_LIST_LABELS`, and repeatable command-line flags.
