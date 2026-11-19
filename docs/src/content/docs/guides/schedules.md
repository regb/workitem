---
title: Schedule recurring work
description: Start a new item on a cron schedule.
---

A schedule starts a new item in a repository on a cron schedule. Each firing is an ordinary item, created and started exactly as if you had sent its prompt from the dashboard: its agent starts with the prompt, the prompt becomes the item's description, and its title is generated from the prompt unless the schedule sets one. Schedules live in the daemon, so they fire without any client open.

```bash
wi schedule add --repo workitem --cron "0 9 * * 1-5" --agent claude \
  --prompt-file triage.md --title "CI triage {{date}}"
wi schedule                 # list schedules, their next and last runs
wi schedule run 01M7        # fire now, without changing the next run
wi schedule disable 01M7
wi schedule edit 01M7 --cron "30 8 * * 1-5"
wi schedule rm 01M7
```

A schedule has a repository, a cron expression, an agent profile and a prompt, all required, and optionally a title and labels for the items it creates. The prompt is stored in the schedule itself, in the daemon. `--prompt-file` and `--stdin` are only ways to pass a long prompt: the command reads the file or standard input once, when `add` or `edit` runs, and sends its text. The file can live anywhere, even a temporary file. wi does not keep its path or read it again, so later changes to the file have no effect. To change the prompt, pass the new text to `wi schedule edit ID` with `--prompt`, `--prompt-file` or `--stdin`. `wi schedule --json` shows the full stored prompt.

In the web app, schedules are under **Settings → Schedules**, where a toggle enables or disables each one.

## Cron and templates

`--cron` takes the standard five fields, `minute hour day-of-month month day-of-week`, or a descriptor such as `@daily` or `@hourly`, in the daemon's local time zone. `0 9 * * 1-5` is 09:00 on weekdays; `*/30 * * * *` is every half hour.

The title and prompt can use these placeholders, filled in when the schedule fires:

| Placeholder | Value |
| --- | --- |
| `{{date}}` | `2026-11-19` |
| `{{time}}` | `09:00` |
| `{{datetime}}` | `2026-11-19 09:00` |
| `{{weekday}}` | `Thursday` |
| `{{repo}}` | The repository's name |

## When schedules fire

The daemon checks for due schedules every 30 seconds. A firing up to five minutes late still runs. One missed by longer, because the daemon was stopped or the machine asleep, is skipped, and the schedule waits for its next run. Each firing creates a new item, even when an earlier run's item is still open. If the new item cannot start, for example because no deep-work capacity is free, it is still created, and the schedule's last run shows the error.

Editing or enabling a schedule computes its next run from now. Disabling it stops future firings; items it already started are unaffected, as they are when you remove it.
