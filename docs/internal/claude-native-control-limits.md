# Claude token streaming and atomic idle controls

TUI investigated on 2026-10-13 against Claude Code 2.1.296. Headless implemented and probed on 2026-10-14 against 2.1.299. Atomic idle guards and native TUI API deltas remain unsupported.

## Per-token output

The supported CLI interface for API text deltas is print mode:

```sh
claude --print --verbose --output-format stream-json --include-partial-messages
```

The SDK exposes the same output through `includePartialMessages` and `stream_event` frames containing `content_block_delta` / `text_delta`. These are API chunks, not a guarantee of one model token per frame.

An isolated interactive launch with `--include-partial-messages --output-format stream-json`, without `--print`, exited with:

```text
Error: --include-partial-messages requires --print and --output-format=stream-json.
```

The probe used private HOME and XDG roots and its own tmux socket/session. It used no copied credentials, sent no prompt, and did not answer dialogs. Evidence: `/tmp/wi-claude-flags-cba7c7u5/screen.txt`.

`MessageDisplay` gives whole-line batches, not raw API deltas. Screen capture cannot manufacture missing token boundaries. Do not proxy authenticated model requests, patch the installed Claude executable, or run a second writer against the native conversation to obtain those deltas.

The authorized headless driver now uses one persistent native streaming-JSON process, with initialization, input UUIDs, replay/queue receipts, consumed-message results, and interrupt receipts. It requires 2.1.299 or later and checks `msg_lifecycle_v1`, `request_marker_lists_v1`, `interrupt_receipt_v1`, and `interrupt_cancel_queued_v1` on each native `system.init`. The initialize response must include a session state. Missing capabilities fail the runtime before model output is accepted. A newer version number alone does not establish protocol compatibility. Private deltas and tool previews are bounded; malformed/truncated frames fail closed. Native storage, multi-turn context, resume, images, abort, cancellation of queued steering/follow-ups, and tmux independence passed isolated probes. A 600-word single-paragraph answer produced many intermediate private snapshots before settlement. The native TUI path is unchanged and still does not expose API deltas.

The Go driver invokes the installed CLI directly, using its existing authentication/settings. It does not implement subscription OAuth or copy credentials. Normal print mode is not bare mode. This implementation makes no claim of an OSS exemption from Anthropic's third-party subscription policies.

Probe evidence: `/tmp/wi-claude-headless-protocol.log`, `/tmp/wi-claude-headless-native.log`, and `/tmp/wi-claude-headless-lifecycle-native.log`. Native frames and transcripts are private and must not be pasted into retained events. Source-tree tests use an allowlisted, synthetic-text fixture in `internal/agentdriver/claude/testdata/`; no raw capture is checked in.

Interrupt success responses must contain `still_queued` and, when queued cancellation was requested, `cancelled` arrays. Missing or null arrays retain an uncertain interruption fence, not a confirmed receipt. Fresh settlement can release the fence without inventing a receipt. Native aborted terminal reasons report failed turns even if the result subtype says `success`. API status codes produce closed authentication/API failure reasons, never native diagnostic text. Shutdown queue reporting shares a one-second deadline so an unresponsive event endpoint cannot add a timeout for each queued input.

## Atomic idle controls

An atomic idle guard must check native turn state and apply or reject a command in the same native operation. It must cover manual input, queued messages, tool continuations, background completions, and cross-session messages. Shutdown must also be conditional at that point, not a host-side status read followed by a signal.

The documented SDK user-message priorities are `next`, `later`, and `now`. They deliver, defer, or interrupt work; none is a reject-if-busy condition. The documented query methods expose interrupt and unconditional close, not conditional idle shutdown. The native inbox reads messages between tool calls or starts a turn when idle. An idle notification is an observation, not a reservation.

No documented native compare-and-submit or compare-and-shutdown operation was found. Adding headless mode supplies API deltas but does not by itself establish an atomic idle guard. Keep `idle_guard=false` unless a native operation with those semantics becomes available and passes race tests.

Driver locks, hook timestamps, prompt IDs, editor readback, and longer grace periods cannot prevent native work from starting between inspection and action. Pausing terminal input would not stop native background producers, and would disrupt the user's TUI.

## Sources

- https://code.claude.com/docs/en/cli-reference
- https://platform.claude.com/docs/en/agent-sdk/streaming-output
- https://platform.claude.com/docs/en/agent-sdk/typescript
- https://code.claude.com/docs/en/cross-session-messaging

Fetched copies are under `/tmp/claude-cli-reference-current.md`, `/tmp/claude-sdk-streaming-current.txt`, and `/tmp/claude-sdk-typescript-current.md`. Absence from the documented interface is not proof that no private internal interface exists; Wi must not claim its guarantees without a supported contract.
