# Automatic review capture

The daemon requests a code snapshot after each accepted `agent.settled` event, regardless of driver, runtime mode, or whether input came from Wi or a native terminal. Bundled Claude TUI/headless and Pi TUI/headless report settlement through their normal event adapters. External drivers use the same semantic event contract.

Capture does not gate dispatch. It is best effort: native continuations, background work, and external writers can race it. A detected checkout change produces a visible capture gap, not a claim of precise execution coverage.

There is no checkpoint capability, version probe, baseline barrier, held-input queue, retry/continue boundary, or confirmation-wait pause. Driver-owned ordinary follow-up queues and native delivery receipts remain independent of review capture. Uncertain delivery is never replayed by capture.

The daemon's app-layer queue runs one capture per item. Settlements arriving during a capture coalesce into one follow-up capture. Different items capture independently. Manual and reply captures share the same per-item capture lock and snapshot function.

`ItemObservation.capturing` travels with `GetItem` and `ListItems`; there is no capture-status RPC or additional browser request. Capturing items are grouped as working in the attention view. Start/end observations wake existing item/list streams. Capture never changes agent busy state or controller admission. Git work holds no item-operation or controller lock; only the short final record transaction takes the item-operation lock. Missing checkouts skip silently. Other failures retain a visible gap/reason and do not block the agent.
