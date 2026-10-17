# Web integration with main

The frontend is rebased onto main `d488742`. All core, model, configuration, coordinator, public protocol, and bundled-driver files match main. The remaining changes are the HTTP/UI implementation, CLI web entrypoint and help, frontend tests, and frontend documentation.

## Main APIs used by the frontend

Main provides registered repositories, readable paginated native history, rich tool/image history, chronological native entry IDs, replaceable live content, image-aware controls in both runtime modes, named agent profiles, model/thinking settings, configuration preflight, runtime ownership/idle guards, headless startup handoff, and archive/shelve partial outcomes.

The frontend does not replace these implementations or bypass their validation. New validates the selected agent before creating an item, matching main's agent-starting workflows. Existing chat remains read-only on navigation and never launches or replaces a runtime.

## Accepted naming decision

Main deliberately removed `item.naming.finalize`. Creation-time slug and implementation branch stay stable even when the display title changes. Its tests explicitly reject the obsolete command.

New now uses main's untitled creation policy. The daemon returns a permanent ID immediately, with an ID-derived slug and branch. Background title generation changes only the display title through `item.edit`. The revision is captured before generation, so concurrent item edits or lifecycle changes reject the background edit rather than overwrite user intent. The original saved prompt remains the initial agent message.

This preserves nonblocking creation, stable links, and a readable display title without a core extension. We accept main's ID-derived slug and branch for now. Smarter naming is deferred; neither prompt-derived names nor an exception to main's naming policy is introduced.

Title generation uses main's configured default auxiliary driver, independently of the selected primary-agent profile. A failed or unsupported request falls back to a short task summary.

## Boundaries and verification

Native conversations remain driver-authoritative. HTTP receipts and optimistic outboxes are access-layer state; unrestricted content never enters compact daemon events. Existing owners are not restarted to gain capabilities.

Tests use isolated HOME/XDG roots, fixture repositories, and fake drivers. The installed Wi binary and live daemon are left untouched.
