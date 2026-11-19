# Item code panel

The conversation page's **Code** button opens an item-scoped panel. Pierre supplies the file tree, source viewer, editor, and unified/split diff renderer. Wi owns the comparison choices, file access, history, saves, commit, and merge. There are no branch-switching, arbitrary-ref, staging, reset, push, or general Git command controls.

## Code and review views

- Change has a live Git view and immutable item-scoped snapshots. Git view keeps an initially empty commit subject/body and a separate title-prefilled squash message. Commit and ordinary Merge remain separate actions; the description is a commit-message body, not the durable item description.
- A Base/Revision header compares actual Git revisions. Start to Working tree is the default; without a checkout it defaults to the preserved item branch tip. Start to HEAD shows committed item work. HEAD to Working tree shows uncommitted changes, including non-ignored untracked files.
- The header lists up to 30 recent commits reachable from the item tip since its starting commit. Base and target can be selected independently. These commits are not saved Gerrit patch sets. A selected commit from History is also available, with Parent as its default base.
- Integration base compares the merge base of the local integration branch and the item branch with the item branch tip. It does not fetch. This remains an item-contribution comparison, not a raw diff of diverged tips.
- The changed-file tree spans the page above the full-width selected diff, rather than sitting beside it. Pierre supplies both. Split diff is the default, with a Unified toggle. Files retains its tree-and-editor browsing layout.
- History shows a topological commit graph for the item branch, local integration branch, and the integration branch's configured remote-tracking upstream. It includes shared ancestry once, with only those branch-tip labels. It never enumerates other branches or fetches. Remote state reflects the last repository sync; an absent upstream or cached tip is explained explicitly. The graph is bounded to 200 commits and marks links continuing outside that window. Selecting any context commit compares it with its first parent. Root commits compare with the empty tree.
- Files shows up to 10,000 tracked and non-ignored untracked paths in the current checkout. It does not enumerate ignored directories or create a checkout.
- Existing regular UTF-8 files up to 1 MiB can be edited and saved. Saving neither stages nor commits them.
- Commit uses `WorkspaceService.CommitWorkspace`, which commits all checkout changes, removes unchanged attachment copies, and requires a stopped agent. It does not stop an agent automatically.
- Merge uses `RepositoryService.IntegrateItem`, captures its input, commits uncommitted work, rebases, and defaults to one squash commit with the old target as its parent. Preserve-series mode fast-forwards the rebased ordinary series. Both allow an idle online agent, refuse a running turn or in-progress capture, and use Wi's default integration target and normal upstream synchronization. Merge neither pushes nor closes the item.

Snapshots, anchored threads, saved drafts, published rounds, replies and optional approvals are implemented as [item-scoped review](revision-aware-review.md). Settlements trigger best-effort capture for every driver/mode without holding input. Capturing arrives with existing item observations and counts as working, without changing agent status or admission. Merge records establish boundaries for continued work; no change-group selector is needed. Published rounds offer an explicit notification action, never a retry worker. A notification failure refreshes canonical publication state without discarding comment buffers. File creation/deletion controls, in-browser conflict resolution and rich binary previews remain outside this panel. PDF files use [native browser links](pdf-links.md), not PDF.js.

## API and ownership

`ReviewService` in `proto/wi/core/v1/reviews.proto` provides live comparisons, retained revision comparisons, discussions, optional intent and explicit notification. `WorkspaceService` provides file reads/saves and ordinary commits. `RepositoryService.IntegrateItem` is the merge command. There is no selected-candidate integration API or capture-status RPC.

Requests name an item, never an arbitrary checkout root. The daemon resolves the registered repository, branch specification, and fenced claim. API handlers translate to `internal/app/review.go`; Git reads use `internal/git/review.go`; bounded filesystem operations use `internal/workspacefiles/`.

`internal/cli/web_review.go` implements the browser backend using generated clients. `internal/web/review.go` provides browser routes behind the existing origin, host, client-identity, request-size, and concurrency checks. The restricted agent endpoint does not expose the new methods.

The wire contract carries ordinary source and comparison data, not Pierre types. New methods are additive within `wi.core.v1`. `ListReviewCommits.include_context` opts into the graph, adding parent SHAs, the selected branch tips and warnings; requests without it retain the original item-only list. Commit selection accepts only ancestry reachable from those same three branch choices, never an arbitrary branch. `GetReview.base_commit` and `GetReviewFile.base_commit` add an optional explicit base within that same scope or the item's immutable starting commit. The resolved base is part of the existing revision fence, so changing it invalidates an old per-file request.

## Revision and save semantics

Review revisions identify resolved base/head commits and the current checkout claim when reading working files. Changing these makes an old per-file request fail with `ABORTED`. A live comparison token is not a frozen snapshot of the working tree; retained `rN` revisions are immutable code snapshots. Each per-file response contains the actual old/new source and their content digests; externally edited files can change between calls.

Workspace reads require a claim ID and generation. Saves require those tokens plus the SHA-256 of the bytes originally read. The daemon takes the item resource-operation lock, validates the current claim and expected branch, and rejects changed content. It writes a temporary sibling, preserves permission bits, checks the original file again, and atomically replaces the directory entry.

Wi serializes its own operations. Agents, shells, and external editors do not take Wi's locks. Digest checks detect observed concurrent edits but cannot provide a filesystem-wide compare-and-swap against an external write between the final check and rename. Users should avoid simultaneously editing the same file with an agent. Save errors retain the browser draft; there is no force-overwrite action or automatic save retry.

Paths must be relative and cannot name Git metadata. Descriptor-relative no-follow opens reject symlinks in every component. Special files and oversized/binary source are not editable. File and Git output sizes and Git process lifetimes are bounded. Review commands disable external diffs, textconv, fsmonitor, and optional Git locks.

## Browser behavior

On desktop, code fills the workspace beside a 600-pixel chat sidebar. The divider resizes chat between 320 and 720 pixels with pointer or keyboard controls; code keeps the remaining width. The sidebar has a shorter composer and a bounded model selector. On mobile, either chat or code occupies the full width, with an explicit Chat button to switch back without losing the composer draft. Panel selection and unsaved file drafts survive closing the panel and navigation within the tab. They do not survive a page reload; a before-unload warning protects outstanding drafts. Drafts from older checkout claims remain available to copy or discard, but cannot be saved into a replacement checkout. Commit subject/body, squash-message and unsaved comment buffers also survive tab changes and closing/reopening the panel within the browser tab, with an unload warning. Only a successful commit clears its matching submitted draft; merge, a no-op commit, errors and late completions do not consume newer text. Pending Git actions stay disabled across panel remounts.

### File references in messages

Explicit Markdown file links and path-like inline code render as file chips. Examples include `src/auth.ts`, `auth.ts:42:3`, `./Dockerfile`, and `[auth](src/auth.ts#L42)`. Bare inline filenames such as `auth.ts`, ordinary prose, fenced code, hostnames, and directory spans ending in `/` remain unchanged. Markdown links can name bare filenames and directories. Attachment links take precedence.

Recognition does not check existence, read files, or open the panel. Clicking resolves the reference against the item's current checkout, opens Files, and reveals the requested line. Repeated clicks reveal the line again. Opening code keeps the clicked reference at its existing visual position as the chat reflows; switching back from mobile code restores that reading position. Unique abbreviated filenames resolve through the workspace file list; ambiguous matches require a choice. A truncated list cannot establish a unique abbreviated match, so use an explicit `./path` in that case. Directory links expand the corresponding tree entry. The chip's context menu offers workspace-open and copy actions.

POSIX absolute paths, Windows paths, `file:` URLs, and home-relative paths are parsed without navigating the browser to the filesystem. Home-relative paths resolve only when the checkout identifies a conventional home root. References outside the claimed checkout and Git metadata paths are refused. Files not in the bounded listing still open by exact path; the core source API determines whether they exist and are readable. This does not bypass its claim fencing or symlink refusal. There is no configured external-editor launcher, so modifier-click does not launch another application.

Only the selected file's source/diff is loaded. Pierre virtualizes code and trees. A shared two-worker pool uses the JavaScript highlighter, with module workers and split assets served by the embedded web client. CSP permits same-origin workers; it does not enable arbitrary scripts or WASM evaluation.

The panel polls while mounted, refreshes on focus and agent-status changes, and supports manual refresh. Polling never replaces an active editor draft. File browsing does not create durable events or notes. Automatic snapshot capture is separate: canonical records retain its progress or gaps, while its in-progress observation groups the item as working.

## Review and consolidation pass

The review found and fixed several recovery failures:

- A checkout change could show the draft's original text instead of its latest edits.
- A late save response could clear a replacement draft at the same path.
- Successful saves and discard/reload could expose stale cached source to a new edit session.
- A failed file listing could hide a retained draft. Draft recovery now works even without a readable checkout.
- Merge could clear an unrelated commit-message draft. Commit completion now clears only the message it submitted.
- A missing checkout could look like a deleted source file. These are now distinct outcomes.
- Recreating a deleted file as untracked could label a modification as an addition.

Regression tests cover these cases, concurrent API saves, branch-ownership checks, cancellation, output bounds, and refusal of FIFOs. The browser test also checks panel reopening, a competing filesystem edit, conflict recovery, discard/reload, and narrow-screen overflow.

Panel state now has one store-owned record rather than six local fields mirrored through an effect. Commit/merge controls own their confirmation and outcome handling in `git-actions.tsx`. The active Pierre editor owns its document in a small edit-session component; its read-only recovery view always uses the latest draft. Source-cache keys and save completion checks are shared rather than duplicated. Git diagnostics have a separate 64 KiB output bound, and cancellation retains its API error classification.

These fixes do not make drafts durable across reloads or make filesystem saves atomic against external writers. The limits described above remain deliberate and visible; this pass is not a production-readiness certification.

## Verification

Core API tests cover editing, stale content/claim/revision errors, literal unusual filenames, committed review without a checkout, history, commit selection, and agent-endpoint restrictions. Adapter tests cover path handling, file types, content limits, permission preservation, staged/unstaged/deleted/untracked changes, and root commits. Frontend tests cover save tokens, conflict recovery, draft restoration, and protecting edits from background refreshes. File-reference tests cover Markdown and inline-code recognition, URL filtering, workspace containment, stale claims, ambiguous and truncated lookups, and repeated line-navigation requests. The Chrome smoke test also checks virtualized line jumps and opening a file chip on mobile without losing the composer draft.

`cmd/wi/e2e_test.go` exercises the production binary and browser adapter, including source read/save, diffs, commit, history, and merge. To also exercise the embedded Pierre components in Chrome, install `agent-browser` and a supported Chrome, build the web client, then run:

```sh
scripts/build-web
REVIEW_BROWSER_SMOKE=1 TMPDIR=/tmp go test ./cmd/wi -run '^TestCLIAPIEndToEnd$' -count=1 -v
```

All daemon, repository, agent, HOME, and XDG state used by this test is isolated from the operator's installation. Normal tests require no browser.
