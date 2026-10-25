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

Open the address printed by the command. Ctrl-C stops serving but leaves agents running.

There is no sign-in: whoever can reach the server controls your agents. On the loopback port that means any process on this machine, which suits a computer only you use. Do not run it this way on a shared machine.

## Access from other devices

To reach the page from elsewhere, put a proxy that authenticates you in front of it. Wi trusts the proxy and serves only what it forwards. Tell Wi the exact address the proxy serves with `--access-url`; requests for any other host are refused, which also defeats DNS rebinding.

Make the proxy the only way in. With `--socket PATH`, Wi serves on a Unix socket that only your user can open (mode `0600`), so neither other local users nor browsers can reach it directly. The proxy must then run as root or as you. Where the proxy cannot open a Unix socket, use a fixed loopback port instead and remember that every local process can reach that port.

When the proxy names the signed-in user in a header, pass `--identity-header NAME`. Wi then refuses requests without it, and names a tab's control lease after the user, so the others see whose tab controls an agent. Trust it only when nothing but the proxy reaches the server: anything that can reach Wi directly could set the header itself.

Which proxy to choose:

| Option | Reaches the page from | Who can read the traffic |
| --- | --- | --- |
| [Tailscale](#tailscale) (recommended) | your devices on your tailnet | only your devices |
| [Cloudflare Tunnel](#cloudflare-tunnel) | any browser, after an Access sign-in | your devices and Cloudflare |
| Another authenticating reverse proxy | depends on the proxy | depends on where it terminates TLS |

Any proxy that authenticates you, forwards to the socket (or loopback port) without changing the Host header, and optionally names the user in a header works the same way.

### Tailscale

`tailscale serve` publishes the page to your tailnet only, over WireGuard, and Tailscale's access controls decide which users and devices reach it. On Linux, `tailscaled` runs as root and can forward to a socket:

```bash
wi web --socket "$XDG_RUNTIME_DIR/wi-web/web.sock" \
  --access-url https://workstation.example.ts.net:8448 \
  --identity-header Tailscale-User-Login
tailscale serve --bg --https=8448 "unix:$XDG_RUNTIME_DIR/wi-web/web.sock"
```

The macOS Tailscale app is sandboxed and cannot open your sockets (it answers 502), so on a Mac serve a loopback port:

```bash
wi web --port 8787 --access-url https://workstation.example.ts.net:8448
tailscale serve --bg --https=8448 http://127.0.0.1:8787
```

`Tailscale-User-Login` is set for signed-in users, not for tagged devices; leave out `--identity-header` if those must reach the page. `tailscale serve --bg` persists its configuration across `wi web` and Tailscale restarts. While `wi web` is stopped, the proxy returns a gateway error. Never use `tailscale funnel`, which publishes to the Internet. `--access-url` also accepts an `http://` origin, for example a tailnet-only `tailscale serve --bg --http=8448 …` when the node cannot obtain a certificate; tailnet traffic is still encrypted, but the browser treats the page as not secure.

### Cloudflare Tunnel

A tunnel reaches the page from any browser, behind a Cloudflare Access sign-in. Protect the hostname with an Access application and a policy that admits only you, then route it to the socket in `cloudflared`'s configuration:

```yaml
ingress:
  - hostname: wi.example.com
    service: unix:/run/user/1000/wi-web/web.sock
  - service: http_status:404
```

```bash
wi web --socket /run/user/1000/wi-web/web.sock \
  --access-url https://wi.example.com \
  --identity-header Cf-Access-Authenticated-User-Email
```

Access adds the signed-in user's email to every request it admits. If the hostname is not covered by Access, the header is missing and Wi refuses every request. Cloudflare terminates TLS, so it can read everything the page shows, including conversations and code. Tailscale cannot.

## Using the MVP

The dashboard at `/` uses the same section grouping and attention ordering as `wi list`, with NEEDS ATTENTION first. Each row shows the title, what a waiting item waits for, deep work, labels and the repository, with Start for a backlog item and Close otherwise; on a phone the row becomes a compact card with no horizontal scrolling. Unread titles are bold. Waiting items appear under WAITING unless an unread response puts them in NEEDS ATTENTION. WAITING, BACKLOG and CLOSED start folded, showing only their name and count; click a section's name to unfold it. The box for adding to the backlog stays below its section, folded or not. CLOSED lists the 50 most recently updated closed items with their resolution and a Reopen button, which starts the item again like `wi reopen` and opens its conversation. Unfolded, it has a search box over every closed item's title, labels, description, and notes, like `wi search --state closed`; a description or notes match shows an excerpt. A closed item's conversation still opens, read only, with Reopen in place of the message box. Agent and worktree status badges are omitted because the section already supplies the primary status context. Less common metadata and lifecycle actions stay in the conversation's Details drawer and action menu.

The Projects multiselect starts with all projects selected. Select one or several projects to narrow the list and attention navigation. Select all includes new projects automatically; Clear all shows an empty list. Selection is kept when navigating between the list and a conversation in the same page. Projects use repository identity, so repositories with the same folder name remain distinct.

Click a work item to open a full-screen conversation. The shared minimal header provides Dashboard, New where appropriate, and one attention control. A positive attention count is clickable and opens the next item under Wi's configured attention policy and project filters. Zero attention is shown in green as “Nothing needs attention” and is not clickable. The conversation menu contains Details, Mark unread, Start for a backlog item, Reopen for a closed one, Shelve, Close as completed, and Close as abandoned. Lifecycle actions and takeovers ask for confirmation in a dialog; successes appear as short notifications and failures stay in an error bar until the next action. Details opens the ticket metadata in a drawer and stays closed by default. It renders the description as Markdown, showing its first few paragraphs, and Read the full description opens the rest in a dialog, as a Markdown attachment opens. Its Waiting section lists what the item waits for, with Clear buttons, and a form to wait for a time, on another item, or for something external. Shelve stops an idle agent, safely releases the checkout, and returns the item to backlog. Close runs Wi's close workflow and records the resolution. An unread conversation opens on your last message, so you read the agent's answer from its top; a read one opens where you left it, or at the end if you left it there or never opened it in this tab. Places last for the tab, across reloads, and the last few conversations keep the older messages you loaded. Opening a conversation marks it read, and so does a new response arriving while you follow the bottom of a visible chat, or leaving a chat whose end you followed after something arrived; Mark unread undoes an accidental open. User messages align right and agent messages align left. Once a turn is over, the agent's work before its final answer folds into one line counting its tool calls and messages, such as “12 tool calls, 2 messages”; click it to see that work as it streamed. The turn in progress shows everything until its answer is final. The client renders GitHub-flavored Markdown, including tables and highlighted code blocks, without accepting conversation HTML. A reply stopped before it finished shows a red Aborted mark; the next message continues as usual. A message naming one of the item's image or Markdown attachments, by its name or a path ending in it such as `/tmp/REPORT.md`, links the name; clicking it opens the attachment in the page, as it opens from Details. Each tool call is one folded line saying what it did: a shell command by its description or the command, a file read, edit or write by its path (an edit with its added and removed line counts), a search by its pattern, a web search or fetch by its query or address. Expanding it shows the work in its own form: a terminal with the command and its colored output, a diff, the file written, the results. Long output shows its first 20 lines until you ask for all of them. Other tools show their indented input and output. A running tool updates the same line when it completes instead of adding a second result message. A compaction of the agent's context shows as a folded “Context compacted” block with the context size before it; it stays between turns rather than folding into one, and opening it shows the summary the agent kept, with Pi's read and modified file lists. A summary of a branch the conversation left shows the same way. A shell command you ran in the agent's own prompt, such as Pi's `!ls`, shows on your side, marked “You ran”, with the command and its output in view. With Claude, a subagent is a card naming its task, type and state; opening it shows the prompt and the subagent's own reasoning, tool calls and result, read when you open it and refreshed while it runs. When a background subagent finishes, a line reports it and opens on the result it returned.

The chip next to the message box shows the conversation's model and reasoning level. It opens the driver's list of models and the levels each one accepts. Applying saves them for this item: every runtime that starts or resumes the conversation uses them, and Reset to profile clears them. A running agent switches at once where its runtime supports it (Pi in both modes, Claude headless). Otherwise a dialog offers to restart the agent now on the same conversation, which needs it idle, or to apply on its next start. Changing a running agent needs control, like sending a message.

Each browser tab is its own [control client](../../concepts/agents/#controllers-and-observers). Opening a conversation only observes it. The first message takes control when nobody holds it: the tab's lease is renewed while the conversation stays open and expires about 45 seconds after the tab closes. If the runtime is offline, sending starts it headless; if it is a keystroke-driven TUI (Claude), sending restarts it headless on the same conversation. When a terminal or another tab controls the agent, a bar says so and offers Take control; sending asks to confirm first, listing what takeover will do (make the terminal read-only, abort the current turn, or restart the runtime). The send button shows what a click does. While the agent is idle it sends, only while the agent is idle: atomically when the driver has an idle guard, otherwise against the observed status. While the agent works it steers the turn in progress when the driver supports steering, and with an empty message box it becomes Stop. ⌘-click or ⌘Enter (Ctrl elsewhere) queues the message for the end of the turn instead; it is offered only while the agent works, since the server refuses to put a follow-up into an idle runtime where it could wait forever. ⌘⌥-click or ⌘⌥Enter sends and, once the server has taken the message, moves on to the next item needing attention, as Done does; a refused message keeps you on the item. The caret joined to the send button sets what a click does: send (or steer), queue next, or send and go to next. The browser keeps that choice; Enter always sends or steers, and queue mode sends normally to an idle agent. The button's icon shows what a click will do, holding the modifiers switches it to their action, and its tooltip lists the shortcuts. On a phone, the caret is how to queue or move on. A queued follow-up is not part of the conversation yet: it waits in gray above the message box as “Follow up: …” and joins the conversation when the agent takes it at the end of the turn. The browser shows a user bubble before the HTTP request finishes and keeps it until native history contains the message. A known refusal leaves the bubble marked Not sent. A dropped connection leaves it marked Delivery unknown, with the text available to edit and retry after checking history. There is no automatic retry.

## Starting conversations

New is a centered chat-style composer: choose a repository, write a message, select a configured agent profile from the discreet unlabeled control below the text box, and Send. There is no title field or larger item-creation form. A current conversation's repository or sole filtered repository is preselected unless you already have a draft. A single known repository and the `default` agent profile are selected automatically. The form exposes only profile names and labels, not driver configuration or credentials. Drafts and the selected profile survive navigation within the page.

Send validates the selected agent configuration before saving the work item, agent profile, and prompt through the daemon and records active intent. The server returns a receipt once those steps are durable, then generates a title, materializes a managed checkout, starts the selected agent headlessly, and submits the saved prompt in the background. The composer clears, retains the repository and agent, and focuses the message box again. Enter sends; Shift+Enter inserts a newline, both here and in an existing conversation. A six-second "Work item started" toast links to the conversation. That link uses the immutable work-item ID returned after durable creation, so it works before Pi generates the title or initializes the primary conversation. Opening the link while startup is pending shows a centered loading wheel only. History and live reads begin when startup finishes; genuine startup failures remain visible. The toast confirms saved active intent, not a completed agent startup or turn. The page never automatically switches to the new conversation. An Attention badge uses the existing list policy; startup problems have an explicit View conversation link.

Pi generates the display title with a separate, bounded print-mode model request using the default auxiliary driver. That request has no tools, extensions, project context, or persisted session. Creation uses main's untitled policy and assigns the permanent ID immediately, with an ID-derived slug and implementation branch. After generation, a revision-checked `item.edit` updates only the display title. The revision is captured before generation, so concurrent edits or lifecycle changes block the background edit and startup. The slug, branch, permanent ID, and source commit never change. If title generation fails or is unsupported, a short task summary is used instead. The original prompt is submitted unchanged to the primary agent. A startup receipt records handoff, not turn completion. Immediate native rejection is reported as a startup problem; subsequent runtime/model failures use normal agent observations.

The repository picker reads Wi's repository registry, including repositories with no items and those hidden by item label filters. It displays current registered names; selection and attention filtering use permanent repository IDs. New resolves the selected repository's owned bare store, integration-branch commit, and default labels through the daemon. It does not depend on the external onboarding checkout. Register repositories through `wi repo add NAME --from SOURCE`, then reopen New.

Startup is bounded to four concurrent jobs and 32 outstanding requests. Receipts are rebuildable, bounded server-memory metadata, not durable jobs. Stopping the web server interrupts unfinished startup without aborting already-running primary agents. The saved active item remains actionable through Wi's normal observations, and startup can be continued with the CLI. Server restart does not silently restart agents.

If startup or initial delivery fails, the item remains and the composer stays ready for another message. While its conversation is still empty and no agent runs, the conversation says the task was never sent and offers Send task, which sends the saved prompt as the first message; this also covers startup that a server restart interrupted. Nothing is resent automatically. Background startup never answers an approval itself: when the agent needs one, such as trusting a worktree `.envrc`, startup stops before the agent starts, and Send task asks in the browser. Inspect the retained conversation rather than resending it as a new task. If the HTTP response is lost or creation is uncertain, the draft is preserved and retry is blocked until you explicitly confirm you checked the list. There is no automatic creation retry or exactly-once HTTP guarantee.

A backlog ticket shows Start in the work list and the conversation menu. Start works like `wi new --start`: the item becomes active, its agent starts headless, and it receives the configured `agent.startup_message`, which tells it to read the item's description and work on it. While it starts, the item shows as starting under IN PROGRESS. To give the agent your own first message instead, open the ticket: a backlog conversation has the same message box as an active one, with attachments and the model choice, and sending starts the item with that message as the first prompt. An active ticket without a live runtime starts headless when you send it a message.

Starting an agent can need your approval, such as trusting a worktree `.envrc` when the repository's `plugins.direnv.trust` is `prompt`. The browser asks: Allow, Start without it (the agent starts without that environment), or Cancel. `wi web` never asks on its own terminal. To skip the question for a repository, set `wi repo settings set NAME plugins.direnv.trust always`.

The CLI equivalents are:

```bash
wi start --agent-mode headless item-slug
wi resume --agent-mode headless --item item-slug
# For active items with no live owner:
wi agent runtime ensure --mode headless --item item-slug
```

Do not ensure a headless runtime while a TUI owner is active; take control from the browser instead, which stops the TUI first.

Installing this version does not update drivers already running. Those owners remain untouched. Readable history still works with the new history reader; live output and idle-only Send require the updated driver. Existing owners can accept explicit follow-up if they advertise that control. Wait for a turn to settle and explicitly stop/restart the owner when you want the new functionality. Close confirms intent and invokes the close safety workflow with force disabled. Dirty or branch-mismatched workspaces and busy agents block closing. An idle agent may shut down before a later cleanup step fails. The page reports retained shutdown/terminal-cleanup warnings and re-reads durable lifecycle after an attempt. It never rolls those steps back or claims failure means nothing changed. Checkout-claim release is committed with the closed manifest, so a failed final save can leave the claim assigned even after terminal cleanup.

## Current limits

- Bundled Pi implements readable history and live content. Other drivers need the optional public APIs and must report a structured control channel to accept web messages; keystroke-only TUI runtimes are restarted headless when a tab takes control.
- The page renders GitHub-flavored Markdown (headings, emphasis, links, nested lists, quotes, tables, task lists, inline and fenced code with syntax highlighting, and HTTPS or inline images). It never renders raw HTML, and drops unsafe link URLs. Non-image attachments are not implemented. Load older messages requests earlier pages of the selected native branch.
- History follows one native entry branch. Each page covers up to 200 native entries, 16 KiB per text or tool field, 1 MiB total text, and 12 MiB image data. It defaults to the latest page and has no conversation-size cutoff. The reader joins tool results to their matching calls; the browser joins calls and results across loaded pages. Legacy records without tree IDs are refused with a warning. The reader never rewrites native content.
- The TUI content endpoint reports its in-memory leaf. Without that endpoint, the history reader selects the last persisted leaf and warns that an older TUI's current tree position may differ.
- Live output is a replaceable preview of the currently streaming assistant message, bounded to 256 KiB. Tool start, update, and completion replace one live disclosure; input and partial or final output fields are each bounded to 64 KiB. Each visible tab holds one event stream for its current view; a hidden tab releases it and reconnects when shown again, because over plain HTTP a browser allows only six connections per host. The server sends a frame only when its content changes: the work list (computed once for all tabs), background creation receipts, and for an open conversation its live output (checked every 400 ms) and the conversation itself (re-read every three seconds, and at once when the agent saves a message or changes activity). Saved messages come only from native history; because history is re-read as soon as a message is saved, a completed preview is replaced without a gap.
- The existing-chat composer accepts pasted or file-picked PNG, JPEG, GIF, and WebP images, up to four images and 8 MiB decoded total. A preview appears before send. Wi passes base64 images directly to Pi's native RPC in headless mode and to its extension content API in TUI mode. Image-only messages work in both modes. Pi's native TUI clipboard-image handling is unchanged. Persisted conversation history renders the same images inline. Clicking an image in the conversation, sent or from the agent, opens it full screen: the buttons, a pinch, Ctrl with the wheel, or a double tap zoom it, dragging pans it, and Escape, the close button or a tap beside it closes it. Initial new-work creation and non-image files do not accept attachments yet.
- Every frame is a full snapshot, never a delta, so reconnecting loses nothing: a new connection starts with the current list, receipts, conversation and live output, and the page reconnects with backoff. Optimistic user messages remain in tab session storage until matching native history arrives, but another browser or tab cannot see that local delivery state. Image bytes remain only in memory until native history confirms them because tab storage quotas are too small.
- Multiple tabs share the same primary conversation. They have separate drafts and separate control leases; only one tab controls at a time.
- No implicit start on navigation, forced lifecycle operations, or remote tools. Runtime replacement happens only when a tab takes control. Private proxy access is supported only through an explicitly configured exact origin.

## Run as a service

On a machine that serves the page continuously, let the service manager own both the daemon and `wi web`, as the user who owns the Wi data, never as root. Set `[daemon] managed = true` in `config.toml` first (see [Managed daemon](../../reference/configuration/#managed-daemon)), so that no `wi` command starts or replaces the supervised daemon. With systemd user units:

```ini
# ~/.config/systemd/user/wi-daemon.service
[Unit]
Description=wi coordinator daemon

[Service]
ExecStart=/path/to/wi daemon serve
Restart=on-failure
KillMode=process

[Install]
WantedBy=default.target
```

```ini
# ~/.config/systemd/user/wi-web.service
[Unit]
Description=wi web
Requires=wi-daemon.service
After=wi-daemon.service

[Service]
ExecStart=/path/to/wi web --socket %t/wi-web/web.sock --access-url https://workstation.example.ts.net:8448 --identity-header Tailscale-User-Login
Restart=on-failure
KillMode=process

[Install]
WantedBy=default.target
```

Agents started through either service live in its control group. `KillMode=process` stops only the service's own process, so restarting the daemon or `wi web` leaves running agents alone. Use one Wi build for both services and the command line. Enable them with `systemctl --user enable --now wi-daemon wi-web`, keep them running without a login session with `loginctl enable-linger USER`, and point the proxy at the socket as above (`%t` is `$XDG_RUNTIME_DIR`).

## Security boundary

Wi does not authenticate. The server binds only to `127.0.0.1` or to an owner-only Unix socket, and whoever reaches it has operator authority to read conversations, create tickets in known repositories, and submit coding-agent work. Authentication belongs to the proxy in front of it. Requests must have the exact configured Host; without `--access-url`, that is the loopback listener. A request with a foreign Origin, or that the browser marks as cross-site, is refused, and mutations require same-origin JSON. Together these keep other websites open in your browser from reading or acting through Wi, including by DNS rebinding. There is no CORS support or state-changing GET.

The client is a React application embedded in the `wi` binary. It never renders conversation HTML: Markdown is turned into React elements, with raw HTML skipped. CSP allows scripts only from the embedded bundle and forbids frames and forms; inline styles are allowed for component positioning and syntax highlighting. Images may load from validated data URLs or HTTPS links in agent output; opening an external image discloses the browser's network address to that image host. Content responses use no-store, no-referrer, and nosniff. Request, transcript and live-preview sizes, active requests, streams, and timeouts are bounded. The access server connects to the operator daemon; the restricted agent endpoint cannot serve this UI.

This is not a defense against a compromised browser profile, another process acting as the same Unix user, another local user when serving a loopback port, or anyone the proxy admits. Public Internet hosting without an authenticating proxy is unsupported.
