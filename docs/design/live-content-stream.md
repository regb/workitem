# Live content stream

Status: implemented (phases 1–4).

## Problem

Browsers see an agent's work through two polls: `wi web` reads the driver's
content snapshot every 400 ms and re-reads history every 3 s, or sooner when
the snapshot's leaf or activity changes. Polling adds latency, repeats whole
snapshots, and leaves the client guessing when live text became history.

Each driver already reads one native event source (Pi RPC events, the Pi TUI
extension, Claude stream-json, Claude hooks and transcript) and projects it
twice: compact facts relayed to the daemon, and a content snapshot. The
projection is shared; the transport for content is not a stream.

## Decision

One ordered content stream per runtime, owned by wi, served by the driver on
its existing content socket. The daemon stays as it is.

```
driver ── one ordered event source ──▶ driver host
                                         ├─ compact facts, no content ──▶ daemon (unchanged)
                                         └─ content events + snapshot ──▶ content socket subscribers
                                                                            (wi web, later the CLI)
```

- The daemon keeps relaying compact semantic facts and persisting them. No
  native content enters the daemon, and a daemon restart does not interrupt
  live views.
- The content socket keeps answering one-shot snapshot reads. A subscriber
  asks for a stream and receives a snapshot followed by numbered events.
- `wi web` holds one subscription per open item and fans it out over its
  per-tab server-sent event stream.
- History stays canonical. Live content is ephemeral and bounded.

### Why wi-owned rather than ACP

ACP adapters exist for Claude and Pi, but they own the agent process, cannot
attach to a running TUI, and remove launch control wi relies on. Browser
libraries for ACP do not exist. Since wi writes every emitter itself, the
format is wi's, with ACP v2 semantics where they fit so a bridge in either
direction stays mechanical:

| wi content event    | ACP v2                                   |
| ------------------- | ---------------------------------------- |
| `message.started`   | first `agent_message_chunk` of a message |
| `message.delta`     | `agent_message_chunk` (append)           |
| `message.replaced`  | `agent_message` (replace accumulated)    |
| `message.completed` | end of message                           |
| `message.saved`     | wi concept: native history entries       |
| `thought.delta`     | `agent_thought_chunk`                    |
| `prompt.saved`      | wi concept: a sent prompt's entries      |
| `tool.started`      | `tool_call`                              |
| `tool.updated`      | `tool_call_update` (in progress)         |
| `tool.completed`    | `tool_call_update` (completed or failed) |
| `tool.cleared`      | wi concept: no current tool calls        |
| `activity`          | wi concept: activity text and branch leaf |
| `turn.settled`      | prompt response stop reason              |

## Protocol

The request gains `stream`. A server that streams answers with a `snapshot`
event, then sends every later event on the same connection, one JSON object
per line. A server that predates streaming answers with a bare snapshot and
closes; clients then fall back to polling.

```json
{"protocol_version":1,"runtime_id":"…","work_item_id":"…","stream":true}
{"sequence":41,"type":"snapshot","snapshot":{…Content…}}
{"sequence":42,"type":"message.started","message_id":"live-42"}
{"sequence":43,"type":"message.delta","message_id":"live-42","text":"Hel"}
{"sequence":44,"type":"tool.started","tool":{"id":"t1","name":"bash","status":"running","input":"…"}}
```

Rules:

- Sequences increase by one. A gap means events were lost: the subscriber
  reconnects and starts again from a snapshot.
- A subscriber that cannot keep up is disconnected, never blocked on.
- Applying events to a snapshot yields the next snapshot. The server computes
  its own snapshot this way, and `driverprotocol.ApplyContent` is the shared
  definition clients use.
- Text is bounded as before (256 KiB per message, 64 KiB per tool field).
  Events carry deltas; a replacement carries the bounded whole text.
- Drivers that publish whole snapshots (transcript-backed Claude TUI, Claude
  headless) keep doing so: the server turns an extension of the current text
  into `message.delta`, and anything else into `message.replaced`.

## Linking live text to history

A finished message keeps its text in the snapshot until the next message
starts. Once the driver knows which native entries hold it, it calls
`ContentServer.MarkSaved(text, ids)`, which publishes `message.saved` only
while the live text is that text, so a late report cannot attach to another
message. The snapshot's `saved_as` holds the ids until the text changes.

Readers show linked text until history contains one of its entries, and
unlinked text until history's last message has the same text or the turn is
idle. When linked text leaves the snapshot before history shows it (the next
message started), the browser holds it until history catches up, for at most
a minute. Finished text therefore never blinks out, and is never shown twice.

How each driver learns the entry:

- Pi headless: Pi appends the session entry after dispatching `message_end`,
  so on its next event the driver finds the entry in the session file by the
  role and millisecond timestamp Pi records in both.
- Pi TUI bridge: after the next task, the same match in the session manager's
  branch.
- Claude headless: the assistant frame carries the transcript record's uuid.
- Claude TUI: the transcript feed knows the record that holds the text.

## Richer events

- Tool calls: the snapshot keeps every call of the current message in order
  (`tools`, bounded to 32), so parallel calls show together; `tool` stays the
  latest for older readers. The browser lays live state over calls history
  already shows and lists the others.
- Reasoning: `thought.delta` appends to a bounded `thought` (newest 64 KiB),
  shown collapsed. Models that keep reasoning private still report a
  `Thinking` activity (Pi `thinking_start`, Claude thinking blocks).
- The Pi TUI bridge sends full tool events, as the Go drivers do.

## Readers

- `wi web` holds one subscription per open item and fans it out to tabs as
  `live` snapshot frames followed by `content` event frames; tabs that fall
  behind get a fresh snapshot. Runtimes whose driver cannot stream are polled.
- `wi agent monitor --content` follows the same stream in a terminal, or as
  one JSON event per line, waiting for and following each new runtime.

## Structured changes

The list, agent status, leases and history are not content, so they are not
on the stream. The daemon keeps an in-memory change log instead: every
committed event, item command, imported conversation batch and controller
handover bumps a revision and records the item (repository changes and
blocker sweeps record every item), and so does an observation pass that
finds an item's status, process or worktree changed. `changes_wait` returns the items changed
after a revision, or waits up to 30 s for one; an epoch per daemon instance
makes a restart read as "everything changed".

`wi web` runs one watcher while any tab is open. It re-reads the list once
per change and sends it only when its encoding differs (list rows carry no
activity ages or manifests, so an unchanged list encodes identically), and
each tab re-reads its conversation only when its item changed. A change
read answers from the daemon's observations; only the 10 s fallback, which
covers what nothing records (lease expiry), asks the daemon to inspect Git,
tmux and processes again. A daemon that cannot report changes is polled as
before.

## Daemon lock

The daemon serializes structured writes under one lock, and every runtime
event waits for it, so nothing slow runs under it. Inspection (Git status in
parallel, one tmux pane listing, one process table read) and conversation
decoding (a driver process) run before the lock; the lock covers only
folding their results into projections. An import applies only if the
checkpoint it started from is still current, so concurrent imports never
apply records twice. Measured on a live data root, runtime events went from
a 126 ms median (p90 650 ms) between emission and commit to under 1 ms, and
`wi list` from about 1 s to 0.17 s.

Navigation inspects nothing itself: `wi next` ranks from the observations
the background passes keep fresh (processes every 2 s, Git every 10 s) after
importing new conversation records, and switching reads one process with
`ps -p` rather than the whole table. In a test client, `wi switch` reaches
`switch-client` in about 55 ms (from 137 ms) and `wi next` in about 85 ms
(from 117 ms, 1 s or more before the lock changes).

## Phases

1. Protocol events, subscription and `ApplyContent`; streaming in the shared
   Go content server and the Pi TUI bridge; `wi web` fan-out and the browser
   reducer, with polling as the fallback.
2. Native identity: `message.saved` and `saved_as`, from all four drivers;
   the browser keeps finished text until history shows it.
3. Several concurrent tools, shared reasoning and a thinking activity, and
   Pi TUI tool events.
4. `wi agent monitor --content`.

## Linking sent prompts

The browser sends each message with its own id, which `wi web` passes as the
control command's request ID (`web:<id>`). Drivers that can tell which native
entry holds a prompt publish `prompt.saved` with that request ID, and the
snapshot keeps the last eight links (`prompts`). The browser confirms a
linked message only when history shows its entry, never by text; text
matching remains for drivers that cannot link.

- Claude headless: wi chooses each prompt's record uuid from its command ID,
  so the link is published when Claude confirms receipt.
- Pi headless and the Pi TUI bridge: the driver pairs a saved user message
  with the oldest submitted prompt of the same text, then finds its session
  entry as for assistant messages.
- Claude TUI: input is pasted into the terminal; it keeps text matching.
