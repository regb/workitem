# Claude protocol fixtures

`headless-2.1.299.jsonl` preserves the ordering and selected field shapes from an isolated installed-Claude 2.1.299 probe: initialization, two turns, replay/lifecycle receipts, API deltas, consumed-message results, and an interrupt receipt.

The fixture uses an explicit field allowlist. All identities and text are synthetic. Account details, credentials, paths, timestamps, model/account configuration, usage, native diagnostics, and tool payloads were removed. Do not add raw protocol captures here.

`TestHeadlessSanitizedNativeFrames` checks this known wire shape without installing Claude or making requests. Failure tests construct malformed receipts and synthetic authentication, API, hook, and terminal failures separately. The installed-Claude opt-in runner checks the live backend; a fixture passing does not establish compatibility with every newer Claude release.
