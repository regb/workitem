# Web integration with main

The frontend builds on main's core. Its own code is the HTTP API and event stream (`internal/web`), the backend composition, CLI entrypoint and help (`internal/cli/web_*.go`, `command_web.go`), the React client (`web/`), and its documentation. Core changes the frontend needed go to main as separate commits.

## Main APIs used by the frontend

Main provides registered repositories, readable paginated native history, rich tool/image history, chronological native entry IDs, replaceable live content, image-aware controls in both runtime modes, named agent profiles, model/thinking settings, configuration preflight, runtime ownership/idle guards, headless startup handoff, and archive/shelve partial outcomes.

The frontend does not replace these implementations or bypass their validation. New validates the selected agent before creating an item, matching main's agent-starting workflows. Existing chat remains read-only on navigation and never launches or replaces a runtime. Reading a chat does renew the tab's own controller lease, if it holds one, so a tab that is closed lets control expire.

The page names items by ID only. Because Wi resolves selectors loosely (prefixes, slugs, titles, descriptions), every item route first checks that an item has exactly that ID and answers 404 otherwise; an ID mentioned in another item's description never resolves to that item.

## Accepted naming decision

Main deliberately removed `item.naming.finalize`. Creation-time slug and implementation branch stay stable even when the display title changes. Its tests explicitly reject the obsolete command.

New now uses main's untitled creation policy. The daemon returns a permanent ID immediately, with an ID-derived slug and branch. Background title generation changes only the display title through `item.edit`. The revision is captured before generation, so concurrent item edits or lifecycle changes reject the background edit rather than overwrite user intent. The original saved prompt remains the initial agent message.

This preserves nonblocking creation, stable links, and a readable display title without a core extension. We accept main's ID-derived slug and branch for now. Smarter naming is deferred; neither prompt-derived names nor an exception to main's naming policy is introduced.

Title generation uses main's configured default auxiliary driver, independently of the selected primary-agent profile. A failed or unsupported request falls back to a short task summary.

## Boundaries and verification

Native conversations remain driver-authoritative. HTTP receipts and optimistic outboxes are access-layer state; unrestricted content never enters compact daemon events. Existing owners are not restarted to gain capabilities.

Tests use isolated HOME/XDG roots, fixture repositories, and fake drivers. The installed Wi binary and live daemon are left untouched.
