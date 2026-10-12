---
title: Local web chat
description: Access work items and their existing primary agents from a local browser.
---

`wi web` serves an experimental local work list and full-screen chat:

```bash
wi web
# Or choose a fixed loopback port:
wi web --port 8080
```

Open the private URL printed by the command. It includes a random access secret in its fragment. The page removes that fragment and keeps the secret in the tab's session storage. Refreshing the same tab works; a new tab needs the launch URL. Ctrl-C stops serving but leaves agents running.

The server always binds to loopback. To use a private HTTPS reverse proxy, choose a fixed loopback port and declare the exact browser-facing origin:

```bash
wi web --port 8787 --access-url https://workstation.example.ts.net:8448
```

Configure the trusted proxy to forward that origin to `http://127.0.0.1:8787` without replacing the browser's Host or Origin. Use a private Tailnet service or equivalent access control. The access secret remains mandatory.

## Using the MVP

The dashboard at `/` uses the same section grouping and attention ordering as `wi list`, with NEEDS ATTENTION first. Desktop uses a reduced table. Mobile uses compact cards with no horizontal scrolling: the first row is the title, the second contains the repository, and Settle is a quick action at bottom right. Agent and worktree status badges are omitted because the section already supplies the primary status context. Less common metadata and lifecycle actions stay in the conversation's Details drawer and action menu.

The Projects multiselect starts with all projects selected. Select one or several projects to narrow the list and attention navigation. Select all includes new projects automatically; Clear all shows an empty list. Selection is kept when navigating between the list and a conversation in the same page. Projects use repository identity, so repositories with the same folder name remain distinct.

Click a work item to open a full-screen conversation. The shared minimal header provides Dashboard, New where appropriate, and one attention control. A positive attention count is clickable and opens the next item under Wi's configured attention policy and project filters. Zero attention is shown in green as “Nothing needs attention” and is not clickable. The conversation menu contains Details, Shelve, and Settle. Details opens the ticket metadata in a drawer and stays closed by default. Shelve stops an idle agent, safely releases the checkout, and returns the item to backlog. Settle is the UI name for Wi's underlying archive operation. User messages align right and agent messages align left. The client renders common Markdown without accepting conversation HTML. Tool calls appear as muted, folded disclosures. A running tool updates the same disclosure when it completes instead of adding a second result message.

The browser reuses the current verified TUI or headless owner. It does not change modes, acquire conversation ownership, or interrupt a turn. Send now submits only while the active driver reports its idle guard. Queue next is an explicit choice for a busy turn; the server refuses to put a follow-up into an idle runtime where it could wait forever. The browser shows a user bubble before the HTTP request finishes and keeps it until native history contains the message. A known refusal leaves the bubble marked Not sent. A dropped connection leaves it marked Delivery unknown, with the text available to edit and retry after checking history. There is no automatic retry.

## Starting conversations

New is a centered chat-style composer: choose a repository, write a message, select a configured agent profile from the discreet unlabeled control below the text box, and Send. There is no title field or larger item-creation form. A current conversation's repository or sole filtered repository is preselected unless you already have a draft. A single known repository and the `default` agent profile are selected automatically. The form exposes only profile names and labels, not driver configuration or credentials. Drafts and the selected profile survive navigation within the page.

Send first saves the work item, selected agent profile, and prompt through the daemon and records working intent. The server returns a receipt once those steps are durable, then generates a title, materializes a managed checkout, starts the selected agent headlessly, and submits the saved prompt in the background. The composer clears, retains the repository and agent, and focuses the message box again. Enter sends; Shift+Enter inserts a newline, both here and in an existing conversation. A six-second "Work item started" toast links to the conversation. That link uses the immutable work-item ID returned after durable creation, so it works before Pi generates the title or initializes the primary conversation. Opening the link while startup is pending shows a centered loading wheel only. History and live reads begin when startup finishes; genuine startup failures remain visible. The toast confirms saved working intent, not a completed agent startup or turn. The page never automatically switches to the new conversation. An Attention badge uses the existing list policy; startup problems have an explicit View conversation link.

Pi generates the title with a separate, bounded print-mode model request. That request has no tools, extensions, project context, or persisted session. Creation assigns the permanent ID immediately with provisional naming. After title generation, a revision-checked daemon command finalizes the title, unique slug, and implementation-branch specification together, before any checkout claim or primary conversation exists. It does not rename an actual Git branch. Existing claims, conversations, lifecycle changes, or explicitly chosen branches block finalization; ordinary later title edits leave slug and branch unchanged. The permanent ID and source commit never change. If title generation fails or is unsupported, a short task summary is used instead. The original prompt is submitted unchanged to the primary agent. A startup receipt records handoff, not turn completion. Immediate native rejection is reported as a startup problem; subsequent runtime/model failures use normal agent observations.

The repository picker reads Wi's repository registry, including repositories with no items and those hidden by item label filters. It displays current registered names; selection and attention filtering use permanent repository IDs. New resolves the selected repository's owned bare store, integration-branch commit, and default labels through the daemon. It does not depend on the external onboarding checkout. Register repositories through `wi repo add NAME --from SOURCE`, then reopen New.

Startup is bounded to four concurrent jobs and 32 outstanding requests. Receipts are rebuildable, bounded server-memory metadata, not durable jobs. Stopping the web server interrupts unfinished startup without aborting already-running primary agents. The saved working item remains actionable through Wi's normal observations, and startup can be continued with the CLI. Server restart does not silently restart agents.

If startup or initial delivery fails, the item remains and the composer stays ready for another message. Inspect the retained conversation rather than resending it as a new task. If the HTTP response is lost or creation is uncertain, the draft is preserved and retry is blocked until you explicitly confirm you checked the list. There is no automatic creation retry or exactly-once HTTP guarantee.

Starting or resuming an existing ticket's agent remains a CLI action:

```bash
wi start --agent-mode headless item-slug
wi resume --agent-mode headless --item item-slug
# For already working items with no live owner:
wi agent runtime ensure --mode headless --item item-slug
```

Do not ensure a headless runtime while a TUI owner is active. The browser can access that TUI owner directly when its driver supports the new content protocol.

Installing this version does not update drivers already running. Those owners remain untouched. Readable history still works with the new history reader; live output and idle-only Send require the updated driver. Existing owners can accept explicit follow-up if they advertise that control. Wait for a turn to settle and explicitly stop/restart the owner when you want the new functionality. Web Settle uses the archive workflow and refuses a live owner without the new idle-guard support; it does not silently restart it.

Settle confirms intent and invokes the existing archive safety workflow with force disabled. Dirty or branch-mismatched workspaces and busy agents block settlement. An idle agent may shut down before a later cleanup step fails. The page reports retained shutdown/terminal-cleanup warnings and re-reads durable lifecycle after an attempt. It never rolls those steps back or claims failure means nothing changed. Checkout-claim release is committed with the archived manifest, so a failed final save can leave the claim assigned even after terminal cleanup.

## Current limits

- Bundled Pi implements readable history and live content. Other drivers need the optional public APIs and must advertise `idle_guard` for idle-only controls and web settlement or shelving of a live owner.
- The page renders headings, paragraphs, emphasis, links, lists, quotes, inline code, fenced code blocks, Markdown image links, and bare HTTPS image URLs. It does not render raw HTML. Tables, non-image attachments, nested Markdown structures, and older-history paging are not implemented.
- History follows one native entry branch, up to 200 messages, 16 KiB per text or tool field, and 1 MiB total text. The reader joins tool results to their matching calls before returning history. Files over 64 MiB and legacy records without tree IDs are refused with a warning. The reader never rewrites native content.
- The TUI content endpoint reports its in-memory leaf. Without that endpoint, the history reader selects the last persisted leaf and warns that an older TUI's current tree position may differ.
- Live output is a replaceable preview of the currently streaming assistant message, bounded to 256 KiB. Tool start, update, and completion replace one live disclosure; input and partial or final output fields are each bounded to 64 KiB. The server polls the driver snapshot every 400 ms and sends NDJSON to the browser. History refreshes every three seconds. Saved messages come only from native history. A completed preview can disappear briefly before the next history refresh, and output completed entirely between polls appears in history rather than as a preview.
- The existing-chat composer accepts pasted or file-picked PNG, JPEG, GIF, and WebP images, up to four images and 8 MiB decoded total. A preview appears before send. Pi's native RPC image field uses base64 image content rather than a filesystem path; Wi sends that native format directly. Persisted conversation history renders the same images inline. Initial new-work creation and non-image files do not accept attachments yet.
- Reconnecting replaces the assistant preview and reloads history; it does not replay deltas. Optimistic user messages remain in tab session storage until matching native history arrives, but another browser or tab cannot see that local delivery state. Image bytes remain only in memory until native history confirms them because tab storage quotas are too small. This MVP does not implement a snapshot watermark or lossless event replay.
- Multiple tabs share the same primary conversation. They have separate drafts, not separate runtime owners.
- No implicit start on navigation, forced lifecycle operations, runtime migration, or remote tools. Private HTTPS proxy access is supported only through an explicitly configured exact origin.

## Security boundary

The server binds only to `127.0.0.1`. The bearer secret grants operator authority to read conversations, create tickets in known repositories, and submit coding-agent work. Keep the launch URL private. Authentication applies to all API reads, streams, and mutations; static assets contain no secret. Requests must have the exact configured Host and Origin. Without `--access-url`, that is the loopback listener. Cross-origin and cross-site requests are rejected; mutations require same-origin JSON and the bearer header. There is no CORS support or state-changing GET.

The client uses `textContent`, never conversation HTML. CSP forbids external scripts, styles, frames, and forms. Images may load from validated data URLs or HTTPS links in agent output; opening an external image discloses the browser's network address to that image host. Content responses use no-store, no-referrer, and nosniff. Request, transcript and live-preview sizes, active requests, streams, and timeouts are bounded. The access server connects to the operator daemon; the restricted agent endpoint cannot serve this UI.

This is not a defense against a compromised browser profile, another process already acting as the same Unix user, another authorized Tailnet device, or stolen access secrets. Tab session storage is readable by page scripts, unlike an HttpOnly cookie. The MVP avoids a cookie/session exchange; an HttpOnly session is a possible later hardening step. Public Internet hosting remains unsupported.
