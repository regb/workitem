# Claude startup delivery

## Isolated reproduction

Run the real-binary regression with a short temporary path on macOS:

```sh
TMPDIR=/tmp go test ./cmd/wi -run '^TestClaudeStartupDelivery$' -count=1 -v
```

It creates private HOME/XDG roots, a synthetic Git repository, its own daemon and tmux server, and a fake Claude process. It makes no model requests and uses no credentials or live conversations.

The CLI controller acquires the vacant lease before the startup message is sent. The test observes `pane_dead=0`, `pane_in_mode=0`, and `pane_input_off=1`. This last field caused the original `pane_changed` rejection. Ignoring that field alone is insufficient: tmux also silently discards `paste-buffer` and `send-keys` input while pane input is disabled.

For an input-off pane, the driver uses a private, mode-0600 input file and `pipe-pane -I`. It sends bracketed-paste bytes, verifies the resulting editor text, then sends the resolved submission shortcut through a second input pipe. Keyboard input remains disabled throughout. Existing pipes are not replaced. The server-side pipe guard rechecks the pane PID, dead state, copy mode, and absence of an existing pipe. Normal pane/process-group, keybinding, draft, and image readback checks still apply.

The test also enters copy mode after queue acceptance to force an asynchronous known-unsent rejection. Both the startup job and an in-flight idempotent retry must report that rejection. Separate coverage replaces a real pane's process while retaining its pane ID. Successful TUI and headless startup, multiline task preservation, and retries must each produce exactly one native prompt.

## Acknowledgements and recovery

Queue acceptance is not native confirmation. Runtime events retain `queued`, `submitted`, `confirmed`, `not_sent`, and `uncertain` delivery facts. A runtime-and-command-fenced coordinator projection keeps the latest outcome across daemon restarts. Native confirmation cannot regress to a delayed timeout or queue event.

Claude API sends wait up to 30 seconds for native confirmation. Only confirmation completes a delivery receipt or startup job successfully. A timeout says the handoff was accepted but not confirmed; the driver may still have queued work. It does not cancel or resend that work. Long-waiting follow-ups can reach this timeout too.

Use a stable `client_message_id` when sending through the API. Retrying that identity on the same runtime queries/deduplicates the same command rather than typing another prompt. Reservations cannot move to a replacement runtime. Semantic rejection also overrides old transport-only receipts that incorrectly claimed delivery. When such a receipt predates the delivery projection, the daemon reads the retained compact events for that runtime and command. Normal delivery uses the indexed projection without scanning history.

- For explicit `not_sent`, retain the original task. Known-unsent TUI failures retain its text in the driver's command record. Fix the reported condition and restart the blocked runtime if necessary. Then explicitly submit the original task with a new `client_message_id`. The old identity continues to report the rejection.
- For `uncertain`, or a confirmation timeout, inspect native history and delivery events before any new submission. Do not automatically resend with a new identity. A same-identity retry cannot duplicate the original command on its original runtime.
- No task text or image bytes are added to compact events or the delivery projection. Clients should retain their original request for explicit recovery, especially across runtime replacement.
