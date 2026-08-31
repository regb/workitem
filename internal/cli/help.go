package cli

import (
	"fmt"
	"io"
	"strings"
)

type helpOption struct {
	Flag string
	Text string
}

type helpDoc struct {
	Summary     string
	Usage       []string
	Description []string
	Options     []helpOption
	Environment []helpOption
	Examples    []string
	SeeAlso     []string
}

var helpDocs = map[string]helpDoc{
	"daemon": {
		Summary:     "Run and inspect the authoritative local coordinator",
		Usage:       []string{"wi daemon <start|serve|status|stop|doctor>"},
		Description: []string{"The daemon is the authoritative per-data-root event store and the only writer of canonical state."},
		SeeAlso:     []string{"daemon start", "daemon serve", "daemon status", "daemon doctor", "daemon stop"},
	},
	"daemon start": {
		Summary:     "Start the coordinator in the background",
		Usage:       []string{"wi daemon start"},
		Description: []string{"Starts one detached daemon for the current data root and waits for readiness. Output is appended to a bounded user-only log under the per-data-root XDG state directory."},
	},
	"daemon serve": {
		Summary:     "Serve the coordinator in the foreground",
		Usage:       []string{"wi daemon serve"},
		Description: []string{"Opens the central database and serves the versioned user-only Unix socket until stopped or signaled."},
	},
	"daemon status": {
		Summary:     "Inspect a running coordinator",
		Usage:       []string{"wi daemon status"},
		Description: []string{"Reports daemon identity, socket, database schema, and committed global event sequence."},
	},
	"daemon doctor": {
		Summary:     "Validate the running coordinator and native sources",
		Usage:       []string{"wi daemon doctor"},
		Description: []string{"Checks connectivity, schema metadata, and the latest native Pi session import warnings."},
	},
	"daemon stop": {
		Summary:     "Stop a running coordinator",
		Usage:       []string{"wi daemon stop"},
		Description: []string{"Requests graceful coordinator shutdown."},
	},
	"shutdown": {
		Summary: "Stop all wi runtimes, terminals, and the daemon",
		Usage:   []string{"wi shutdown [--force]"},
		Description: []string{
			"Stops every tracked agent runtime, waits for process exit, closes canonical wi tmux sessions, closes orphaned tmux sessions only when their session environment proves wi ownership, and stops the daemon last. It never uses broad process-name matching or kills the tmux server.",
			"Without --force, busy agents and unavailable control sockets are reported and left running. --force requests abort and shutdown, then signals only process groups whose recorded PID start time and group identity still match.",
			"When invoked from tmux or an agent runtime, wi starts a detached worker so the shutdown can finish after closing the caller's own process and session. The command prints the user-only result log path.",
		},
		Options: []helpOption{{"--force", "Abort busy turns and terminate verified runtime process groups if graceful shutdown times out."}},
		SeeAlso: []string{"agent runtime stop", "terminal close", "daemon stop"},
	},
	"info": {
		Summary:     "Show resolved paths for the current wi data root",
		Usage:       []string{"wi info [key]"},
		Description: []string{"Without a key, prints all resolved data, configuration, state, runtime, and socket paths. With a key, prints only its value for use in scripts. Path resolution is local and does not start or contact the daemon."},
		Examples:    []string{"wi info", "wi info agent-socket", "wi info --json"},
	},
	"version": {
		Summary:     "Show the baked-in build version",
		Usage:       []string{"wi version"},
		Description: []string{"Development builds derive a unique version from Go's embedded Git revision and report dirty state. Release builds may inject a release name at link time; JSON also includes the full revision, commit time, and Go version."},
	},
	"new": {
		Summary: "Create a durable work item",
		Usage:   []string{"wi new [options] <title>"},
		Description: []string{
			"Normally creates a work item in backlog. Durable metadata and DESCRIPTION.md are written immediately; no terminal session, Pi conversation, runtime, or composed use case is started. Ordinary items begin with an absent managed-slot checkout; --home records a borrowed primary-checkout claim without creating or modifying that checkout. Without --base, the immutable created-from commit is resolved from the local default branch; no fetch occurs. If no local default can be determined, current HEAD is used with a warning.",
			"With --prompt, the prompt is persisted as DESCRIPTION.md, the item moves to working, and an unattached agent receives it through runtime control. TUI mode is the default: it creates the normal tmux runtime for later entry but does not switch or attach the caller. Use --agent-mode rpc for an explicitly headless runtime. If startup or delivery fails, the command reports the durable partial outcome instead of deleting the item.",
			"Use --desc-file for a backlog item's self-contained multiline description. Labels are repeatable and augment defaults from user config, <repository>/.config/wi.toml, and WI_ITEM_DEFAULT_LABELS; use --no-default-labels to opt out. Deep work is a built-in attribute, not a label.",
			"A sandbox may set WI_AGENT_DAEMON_SOCKET to the daemon's separately mounted agent endpoint. That endpoint preserves current Git working-directory resolution but permits only absent managed-slot backlog creation; --home and --prompt are rejected.",
		},
		Options: []helpOption{
			{"--desc-file <path>", "Read DESCRIPTION.md content from a file."},
			{"--prompt <text>", "Persist the text as DESCRIPTION.md and immediately start an unattached agent with it."},
			{"--agent-mode <mode>", "With --prompt, start tui (default) or rpc."},
			{"--slug <slug>", "Set the initial active slug; must be unique."},
			{"--label <label>", "Add a label after inherited defaults; repeatable."},
			{"--no-default-labels", "Do not inherit user or repository default labels."},
			{"--deep", "Mark the item as deep work."},
			{"--home", "Borrow the repository's primary checkout; it must be on the local default branch and have no other active home claim."},
			{"--repo <path>", "Create the item for another local Git repository."},
			{"--base <revision>", "Use a specific revision instead of the local default branch; use HEAD for the current checkout."},
		},
		Environment: []helpOption{{"WI_ITEM_DEFAULT_LABELS", "Comma-separated additive default labels."}, {"WI_AGENT_DAEMON_SOCKET", "Use the restricted agent endpoint at an explicit absolute path; it is never auto-started."}},
		Examples: []string{
			`wi new --label follow-up --desc-file handoff.md "Investigate refresh race"`,
			`wi new --prompt "Investigate the race, add a regression test, and report findings." "Investigate refresh race"`,
			`wi new --agent-mode rpc --prompt "Run the audit and report findings." "Audit dependencies"`,
			`wi new --deep --repo ../api "Redesign authentication"`,
			`wi new --home "Mainline maintenance"`,
		},
	},
	"list": {
		Summary: "List and filter work items",
		Usage:   []string{"wi list [options]"},
		Description: []string{
			"Shows active work grouped by actionability. Archived items are hidden by default. --archived selects archived-only, --all includes active and archived, and --state archived implies archived visibility. Live agent/worktree inspection adds AGENT and WORKTREE columns unless --no-agent is used.",
			"Label rules are additive. A bare label or +label requires that label; -label excludes it. All positive labels must match and no negative label may match.",
			"Global defaults use labels = [\"+team\", \"-personal\"] in the [list] section of config.toml. WI_LIST_LABELS accepts comma-separated rules. CLI rules use repeatable --label flags.",
			"Rules merge by source precedence: config list.labels < WI_LIST_LABELS < CLI --label. A higher-precedence rule replaces only the same normalized label while unrelated inherited rules remain. Within one source, rules are processed left-to-right and the later rule wins for the same label. The token ! clears all inherited rules before subsequent tokens are applied.",
			"Within NEEDS ATTENTION, [attention] priority selects the ranking strategy. The current recent-request strategy orders non-deferred items by newest observed user prompt, then deferred items by oldest attention.deferred event. JSON exposes the selected strategy and requested, completed, and deferred timestamps.",
		},
		Options: []helpOption{
			{"--label <rule>", "Apply a label rule; repeatable. Forms: label, +label, -label, !."},
			{"--state <state>", "Filter by backlog, working, waiting, or archived."},
			{"--archived", "Show archived items only."},
			{"--all", "Show active and archived items."},
			{"--ids", "Show ID prefixes in active text output."},
			{"--no-agent", "Skip live agent/worktree inspection."},
		},
		Environment: []helpOption{
			{"WI_LIST_LABELS", "Comma-separated label rules overlaid on config; e.g. +team,-blocked or !,+personal."},
			{"NO_COLOR", "Disable colored text output."},
		},
		Examples: []string{
			`wi list --label backend`,
			`wi list --label +backend --label +jira --label -blocked`,
			`WI_LIST_LABELS="+personal,-jira" wi list`,
			`WI_LIST_LABELS="!,+personal" wi list --json`,
		},
	},
	"show": {
		Summary:     "Show durable task context and metadata",
		Usage:       []string{"wi show [--item <selector>] [item]"},
		Description: []string{"Prints durable metadata and the hydrated DESCRIPTION.md contents. Text and JSON use the same stable result containing manifest and description. If no item is supplied, resolves the current item from WI_ID, tmux, or the current claimed checkout."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
		Examples:    []string{`wi show`, `wi show --item auth`, `wi --json show 01KZ39T`},
	},
	"state": {
		Summary:     "Inspect or change durable lifecycle state",
		Usage:       []string{"wi state <show|set> [options]"},
		Description: []string{"This durable-item primitive changes lifecycle state only. It never creates, enters, inspects, closes, or releases worktree, terminal, or runtime resources."},
		SeeAlso:     []string{"state show", "state set"},
	},
	"state show": {
		Summary:     "Show durable lifecycle state",
		Usage:       []string{"wi state show [--item <selector>] [item]"},
		Description: []string{"Reads backlog, working, waiting, or archived without inspecting workspace or agent resources."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"state set": {
		Summary:     "Set durable lifecycle state without workspace effects",
		Usage:       []string{"wi state set <backlog|working|waiting|archived> [--item <selector>] [--force] [item]"},
		Description: []string{"Performs a legal state transition and appends a durable event. Workspace resources are deliberately left unchanged. Setting archived clears the active slug; setting an archived item to backlog allocates a fresh slug."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--force", "Override deep-work capacity only when setting backlog to working."},
		},
		Examples: []string{`wi state set waiting`, `wi state set working --item auth`, `wi state set backlog auth`},
	},
	"delete": {
		Summary:     "Irreversibly delete archived work-item case files",
		Usage:       []string{"wi delete --yes [--item <selector>] [item]", "wi delete --archived --yes"},
		Description: []string{"Deletes the complete work-item case file, including its description and Pi conversations. Deletion requires archived state and refuses live agent runtimes, tmux terminals, or materialized checkouts. --archived validates every archived item before deleting any of them."},
		Options: []helpOption{
			{"--item <selector>", "Select one archived item explicitly."},
			{"--archived", "Delete all archived items after validating the complete set."},
			{"--yes", "Confirm irreversible deletion."},
		},
		Examples: []string{`wi delete --yes --item 01KZ39T`, `wi delete --archived --yes`},
	},
	"attention": {
		Summary:     "Inspect attention activity, ranking, and deferral",
		Usage:       []string{"wi attention <activity|defer|queue> [options]"},
		Description: []string{"Attention facts and ranking are independent of lifecycle state mutation and workspace navigation."},
		SeeAlso:     []string{"attention activity", "attention defer", "attention queue", "next"},
	},
	"attention activity": {
		Summary:     "Show prompt, completion, and defer timestamps",
		Usage:       []string{"wi attention activity [--item <selector>] [item]"},
		Description: []string{"Shows the latest facts used to rank attention. Timestamps are derived from Pi JSONL and the work-item event log."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"attention defer": {
		Summary:     "Move a NEEDS ATTENTION item to the back of the queue",
		Usage:       []string{"wi attention defer [--item <selector>] [item]"},
		Description: []string{"Records attention.deferred for a working item without inspecting agent or workspace eligibility. It never changes lifecycle state or workspace resources. Most interactive navigation should use wi next --defer, which conditionally defers only a current NEEDS ATTENTION item."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"attention queue": {
		Summary:     "Show the ranked NEEDS ATTENTION queue",
		Usage:       []string{"wi attention queue [--label <rule>]..."},
		Description: []string{"Returns candidates ranked by the configured attention.priority strategy without choosing a successor or entering a workspace."},
		Options:     []helpOption{{"--label <rule>", "Apply the same repeatable label rules as wi list."}},
		Environment: []helpOption{{"WI_LIST_LABELS", "Comma-separated label rules overlaid on config."}},
	},
	"events": {
		Summary:     "Show the durable work-item event log",
		Usage:       []string{"wi events [--item <selector>] [item]"},
		Description: []string{"Prints the compact append-only daemon event journal, including lifecycle, workspace, merge, runtime, and attention.deferred records. JSON output returns structured events."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
		Examples:    []string{`wi events`, `wi events --item auth`, `wi --json events auth`},
	},
	"merge": {
		Summary: "Rebase and fast-forward a local target branch",
		Usage:   []string{"wi merge [--item <selector>] [--target <branch>] [target]"},
		Description: []string{
			"Merges the current working/waiting item without switching checkouts: requires a completely clean source checkout, rebases its persisted implementation branch onto the target, atomically fast-forwards the local target ref, and synchronizes any worktree where the target is checked out.",
			"The target defaults to origin/HEAD's local branch, then main, master, develop, or trunk. No fetch, commit, squash, branch deletion, workspace cleanup, shelve, or archive is performed.",
			"If rebase, target advancement, or target-worktree synchronization fails, wi aborts/rolls back and restores the pre-merge source and target commits. Dirty target files are allowed only when they do not overlap incoming paths.",
		},
		Options: []helpOption{
			{"--item <selector>", "Select the source work item explicitly."},
			{"--target <branch>", "Select an existing local target branch instead of positional target."},
		},
		Examples: []string{`wi merge`, `wi merge main`, `wi merge --item auth --target main`, `wi merge --target develop`, `wi --json merge main`},
	},
	"start": {
		Summary: "Start backlog work, optionally creating a minimal item",
		Usage: []string{
			"wi start [--agent-mode <tui|rpc>] [--force] <slug>",
			"wi start --new [--home] [--no-default-labels] [--agent-mode <tui|rpc>] [--force] <title>",
		},
		Description: []string{
			"Without --new, resolves an exact active slug or one unambiguous active-slug substring. Exact matches take precedence; ambiguous substrings fail with candidate IDs and slugs. IDs, title keywords, implicit current-item selection, and --item are not accepted.",
			"With --new, creates a minimal backlog item from the supplied title, assigns its slug normally, then starts it. If startup fails after creation, the item is retained and identified in the error.",
			"Starting transitions backlog -> working, enforces deep-work capacity, materializes the worktree, and ensures the selected primary runtime. TUI mode attaches terminal access; RPC mode remains headless. JSON never attaches.",
		},
		Options: []helpOption{
			{"--new", "Create a minimal item from the supplied title before starting."},
			{"--home", "With --new, borrow the repository's primary default-branch checkout."},
			{"--no-default-labels", "With --new, do not inherit default labels."},
			{"--agent-mode <tui|rpc>", "Start native TUI (default) or headless RPC mode."},
			{"--force", "Override the deep-work active limit."},
		},
		Environment: []helpOption{{"WI_ITEM_DEFAULT_LABELS", "Comma-separated defaults applied when --new creates the item."}},
		Examples:    []string{`wi start auth-race`, `wi start --new "Fix typo"`, `wi start --new --home "Mainline maintenance"`, `wi start --new --agent-mode rpc "Audit dependencies"`},
	},
	"switch": {
		Summary: "Select and switch to a work item",
		Usage: []string{
			"wi switch [--no-agent] [--no-preview] [--label <rule>]...",
			"wi switch [--item <selector>] <item>",
			"wi switch --current",
		},
		Description: []string{
			"Without an item, opens an interactive fzf picker over working, waiting, and backlog items using the same actionability projection and label rules as wi list. Backlog items appear last in a distinct color. The picker initially selects the current item when present; changing the filter resets the cursor to the first match. Selecting a backlog item starts it, selecting a waiting item resumes it, and selecting a working item is state-neutral before entering its TUI. The daemon refreshes native activity before capturing candidates; the open picker is an explicitly marked frozen snapshot and can be reopened to refresh. A tmux key binding can host the complete operation in a popup so startup prompts and errors remain visible.",
			"With an explicit item, or with --current, performs the existing state-neutral switch directly: enter a verified existing TUI terminal even when its checkout needs repair, otherwise ensure the worktree, primary TUI runtime, and tmux access, then attach or switch the client. JSON requires an explicit item or --current and never attaches.",
		},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly instead of opening the picker."},
			{"--current", "Use implicit WI_ID, tmux, or checkout resolution instead of opening the picker."},
			{"--no-agent", "In picker mode, skip live agent/worktree inspection."},
			{"--no-preview", "In picker mode, disable the durable-details and observed-status preview pane."},
			{"--label <rule>", "In picker mode, apply the same repeatable label rules as wi list."},
		},
		Environment: []helpOption{
			{"WI_FZF", "Override the fzf-compatible picker executable."},
			{"WI_LIST_LABELS", "Comma-separated label rules overlaid on config."},
		},
		Examples: []string{`wi switch`, `wi switch agent-context`, `wi switch --current`, `wi switch --label +backend --label -blocked`},
	},
	"workspace": {
		Summary:     "Inspect and manage the repository worktree",
		Usage:       []string{"wi workspace <status|ensure|release|relocate> [options] [item]"},
		Description: []string{"This core area owns only repository checkout claims and materialization. Tmux access and agent runtimes are separate resources."},
		SeeAlso:     []string{"workspace status", "workspace ensure", "workspace release", "workspace relocate", "terminal", "agent runtime"},
	},
	"workspace status": {
		Summary:     "Inspect worktree and Git condition",
		Usage:       []string{"wi workspace status [--item <selector>] [item]"},
		Description: []string{"Reports the derived worktree condition and reason, expected/current branch, created-from/current HEAD, and dirty state without inspecting terminal or agent resources."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"workspace ensure": {
		Summary:     "Materialize the repository worktree",
		Usage:       []string{"wi workspace ensure [--item <selector>] [item]"},
		Description: []string{"Ensures only the checkout/worktree. It does not create tmux or a Pi conversation. Lifecycle state is unchanged."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"workspace release": {
		Summary:     "Release a clean repository worktree",
		Usage:       []string{"wi workspace release [--item <selector>] [--force] [item]"},
		Description: []string{"Releases only the workspace claim. Managed slots are retained for reuse; repository-home directories are never modified. Active agent runtimes and terminal sessions must be stopped explicitly first."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}, {"--force", "Pass through to managed worktree release where safe."}},
	},
	"workspace relocate": {
		Summary:     "Use a repository after its checkout moved",
		Usage:       []string{"wi workspace relocate --repository <path> [--item <selector>] [item]"},
		Description: []string{"Validates that the replacement repository has the recorded origin and created-from commit, then records it as the current location. The original creation path remains provenance. The item must not have an assigned workspace."},
		Options:     []helpOption{{"--repository <path>", "Select the replacement repository checkout."}, {"--item <selector>", "Select an item explicitly."}},
		Examples:    []string{`wi workspace relocate --repository ~/vcs/new/project --item project-bug`},
	},
	"terminal": {
		Summary:     "Manage optional terminal access",
		Usage:       []string{"wi terminal <status|ensure|enter|close> [options] [item]"},
		Description: []string{"Low-level tmux adapter plumbing operates over an existing worktree. It never creates a Pi conversation or changes lifecycle state."},
		SeeAlso:     []string{"terminal status", "terminal ensure", "terminal enter", "terminal close", "workspace"},
	},
	"terminal status": {Summary: "Inspect optional terminal access", Usage: []string{"wi terminal status [--item <selector>] [item]"}, Options: []helpOption{{"--item <selector>", "Select an item explicitly."}}},
	"terminal ensure": {Summary: "Ensure optional terminal access", Usage: []string{"wi terminal ensure [--item <selector>] [item]"}, Description: []string{"Requires an existing worktree and creates no agent resources."}, Options: []helpOption{{"--item <selector>", "Select an item explicitly."}}},
	"terminal enter":  {Summary: "Enter optional terminal access", Usage: []string{"wi terminal enter [--item <selector>] [item]"}, Description: []string{"Ensures tmux over an existing worktree and attaches unless JSON output is selected."}, Options: []helpOption{{"--item <selector>", "Select an item explicitly."}}},
	"terminal close":  {Summary: "Close optional terminal access", Usage: []string{"wi terminal close [--item <selector>] [item]"}, Description: []string{"Closes tmux only. A TUI agent runtime must be stopped explicitly first."}, Options: []helpOption{{"--item <selector>", "Select an item explicitly."}}},
	"next": {
		Summary: "Switch to the next item in the NEEDS ATTENTION ring",
		Usage:   []string{"wi next [--defer | --wait | --archive] [--label <rule>]..."},
		Description: []string{
			"Builds the ranked NEEDS ATTENTION queue using the same config, WI_LIST_LABELS, and optional --label rules as wi list. If the current item is in that queue, selects its successor and wraps from the last item to the first. If the current item is busy, waiting, needs fixing, backlog, archived, or otherwise outside the queue, starts at the first NEEDS ATTENTION item.",
			"Plain navigation takes one synchronized daemon actionability snapshot, remains state-neutral, and performs the same TUI composition as wi switch. By default it records no defer, selection event, cursor, or ranking timestamp. With --defer, it first records attention.deferred only when the current item belongs to NEEDS ATTENTION; from busy, waiting, fixing, or other outside items it simply navigates.",
			"With --wait, wi atomically moves the current working item to waiting and ranks the resulting daemon actionability snapshot, then navigates from the top. With --archive, wi validates clean-worktree safety and stops the previous runtime before switching, then durably releases its workspace claim and applies archived state before scheduling the previous tmux session for closure. --wait, --defer, and --archive are mutually exclusive. If switching succeeds but archiving fails, the new item remains selected and the error reports the partial outcome.",
			"JSON mode selects and ensures the target workspace without attaching to tmux. Therefore --json next --archive invoked inside the previous item's tmux session refuses terminal cleanup rather than pretending the client switched.",
		},
		Options: []helpOption{
			{"--defer", "Defer the current item if it is in NEEDS ATTENTION before navigating."},
			{"--wait", "Set the current item to waiting before navigating."},
			{"--archive", "Enter a distinct successor, then stop, clean up, and archive the previous item."},
			{"--label <rule>", "Apply the same repeatable additive label rules as wi list."},
		},
		Environment: []helpOption{{"WI_LIST_LABELS", "Comma-separated label rules overlaid on config."}},
		Examples:    []string{`wi next`, `wi next --defer`, `wi next --wait`, `wi next --archive`, `wi next --label +backend --label -blocked`, `wi --json next --wait`},
	},
	"resume": {
		Summary:     "Move waiting work to working and resume its agent",
		Usage:       []string{"wi resume [--item <selector>] [--agent-mode <tui|rpc>] [item]"},
		Description: []string{"Transitions waiting -> working and ensures the selected primary-agent runtime like wi start. Backlog items must use wi start. JSON output never attaches."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--agent-mode <tui|rpc>", "Start native TUI (default) or headless RPC mode."},
		},
		Examples: []string{`wi resume`, `wi resume --item auth`},
	},
	"shelve": {
		Summary:     "Return active work to backlog",
		Usage:       []string{"wi shelve [--item <selector>] [--force] [item]"},
		Description: []string{"Transitions working/waiting -> backlog after inactive terminal/worktree cleanup. Active runtimes must be stopped explicitly; dirty or branch-mismatched worktrees remain protected."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--force", "Allow the state transition while retaining an unclosable current terminal."},
		},
	},
	"archive": {
		Summary: "Archive a work item",
		Usage:   []string{"wi archive [--item <selector>] [--force] [item]"},
		Description: []string{
			"Gracefully stops an idle agent runtime, waits for it to exit, closes terminal access, releases the clean workspace claim, then transitions the item to archived and clears its active slug. Busy runtimes require --force, which aborts before shutdown.",
			"To restore an archived item, run `wi state set backlog --item <item-id>`. Restoration retains its ID and history, assigns a fresh active slug, and does not recreate workspace or agent resources.",
		},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--force", "Abort a busy runtime before shutdown and allow archiving while retaining an unclosable current terminal."},
		},
		Examples: []string{`wi archive add-api-retries`, `wi state set backlog --item 01KZ39T`},
	},
	"label": {
		Summary: "List, add, or remove item labels",
		Usage: []string{
			"wi label [--item <selector>]",
			"wi label [--item <selector>] <label>...",
			"wi label --remove [--item <selector>] <label>...",
		},
		Description: []string{"With no labels, lists labels. Otherwise adds normalized labels, or removes them with --remove. Labels are lowercase organizational metadata; deep work is separate."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--remove", "Remove the supplied labels instead of adding them."},
		},
		Examples: []string{`wi label backend auth`, `wi label --remove --item auth blocked`, `wi label --item auth`},
	},
	"deep": {
		Summary:     "Set or clear deep-work classification",
		Usage:       []string{"wi deep [--item <selector>] [--clear] [item]"},
		Description: []string{"Marks an item as deep work, or clears it. Working/waiting deep items consume configured active capacity."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--clear", "Clear deep-work classification."},
		},
	},
	"agent": {
		Summary:     "Inspect, run, monitor, and communicate with the primary agent",
		Usage:       []string{"wi agent <status|control|runtime|monitor> [options]"},
		Description: []string{"Agent commands derive status from runtime events and expose direct runtime control."},
		SeeAlso:     []string{"agent status", "agent control", "agent runtime", "agent monitor"},
	},
	"agent status": {
		Summary:     "Inspect derived agent and worktree status",
		Usage:       []string{"wi agent status [--item <selector>] [--stale-after <duration>] [item]", "wi agent status --all [--stale-after <duration>]"},
		Description: []string{"Shows busy, idle, or problem for one selected primary agent, separately from absent, clean, changed, or problem worktree status. --all explicitly returns the working/waiting overview, keeping JSON result shapes predictable."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--all", "Show the working/waiting agent overview instead of one item."},
			{"--stale-after <duration>", "Treat old incomplete activity as a problem (default 10m)."},
		},
	},
	"agent control": {
		Summary:     "Submit a low-level command to the owning agent runtime",
		Usage:       []string{"wi agent control send [--follow-up] [--item <selector>] [--actor <name>] [--stdin | --file <path> | <message...>]", "wi agent control <abort|shutdown> [--item <selector>] [--actor <name>]"},
		Description: []string{"Submits directly to runtime control without consulting derived busy/idle status. Send defaults to steering delivery at Pi's next turn boundary; --follow-up waits until the current run settles."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--actor <name>", "Record the control source/actor."},
			{"--stdin", "Read the message from standard input."},
			{"--file <path>", "Read the message from a file."},
		},
		Examples: []string{`wi agent control send --item auth "Focus on the failing test"`, `wi agent control send --follow-up --item auth "Summarize when finished"`},
	},
	"agent control send": {
		Summary:     "Send input with explicit delivery semantics",
		Usage:       []string{"wi agent control send [--follow-up] [--item <selector>] [--actor <name>] [--stdin | --file <path> | <message...>]"},
		Description: []string{"Defaults to steering: while busy, deliver after the current assistant turn and tool calls; while idle, start immediately. --follow-up delivers only after the current run fully settles."},
		Options:     []helpOption{{"--follow-up", "Wait until the current run fully settles."}, {"--item <selector>", "Select an item explicitly."}, {"--actor <name>", "Record the control source/actor."}, {"--stdin", "Read the message from standard input."}, {"--file <path>", "Read the message from a file."}},
	},
	"agent control abort": {
		Summary: "Abort current agent work",
		Usage:   []string{"wi agent control abort [--item <selector>] [--actor <name>]"},
		Options: []helpOption{{"--item <selector>", "Select an item explicitly."}, {"--actor <name>", "Record the control source/actor."}},
	},
	"agent control shutdown": {
		Summary:     "Request low-level runtime shutdown",
		Usage:       []string{"wi agent control shutdown [--item <selector>] [--actor <name>]"},
		Description: []string{"Prefer `wi agent runtime stop`, which performs busy-state safety checks before using this primitive."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}, {"--actor <name>", "Record the control source/actor."}},
	},
	"agent runtime": {
		Summary:     "Inspect or ensure the primary agent runtime",
		Usage:       []string{"wi agent runtime <status|ensure|stop> [options] [item]"},
		Description: []string{"A work item has one durable primary conversation and at most one active runtime owner. TUI uses a thin extension adapter; RPC uses Pi's native stdin/stdout protocol; both hold the same conversation lock."},
		SeeAlso:     []string{"agent runtime status", "agent runtime ensure", "agent runtime stop", "agent monitor"},
	},
	"agent runtime status": {
		Summary: "Inspect runtime mode, process ownership, and capabilities",
		Usage:   []string{"wi agent runtime status [--item <selector>] [item]"},
		Options: []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"agent runtime ensure": {
		Summary:     "Ensure a TUI or headless RPC runtime",
		Usage:       []string{"wi agent runtime ensure --mode <tui|rpc> [--item <selector>] [item]"},
		Description: []string{"TUI mode runs native Pi inside tmux. RPC mode runs headlessly without creating a tmux session. A different live mode must be stopped before handoff."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--mode <tui|rpc>", "Select native TUI or headless RPC mode."},
		},
	},
	"agent runtime stop": {
		Summary:     "Request graceful runtime shutdown",
		Usage:       []string{"wi agent runtime stop [--item <selector>] [--force] [item]"},
		Description: []string{"Sends shutdown through socket control. Busy runtimes are refused unless --force first requests an abort. If socket control fails, --force may signal the process group only when its recorded start time and group identity still match. Runtime handoff is safe after the owner exits."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--force", "Abort before shutdown and terminate the verified process group if socket control fails."},
		},
	},
	"agent monitor": {
		Summary:     "Read compact runtime events",
		Usage:       []string{"wi agent monitor [--item <selector>] [--limit <n>] [--follow] [item]"},
		Description: []string{"Shows the compact lifecycle, message, command, and tool events retained by the daemon."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--limit <n>", "Show the latest N events; zero means all."},
			{"--follow", "Continue streaming new normalized events."},
		},
	},
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "wi - durable local AI-assisted development work items")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  wi [--json] <command> [options]")
	fmt.Fprintln(w, "  wi help [command [subcommand]]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Work:")
	printCommandRows(w, [][2]string{{"new", "Create a work item"}, {"list", "List work"}, {"show", "Show an item"}, {"start", "Start backlog work"}, {"switch", "Open active work"}, {"next", "Open the next item needing attention"}, {"resume", "Resume waiting work"}, {"shelve", "Return active work to backlog"}, {"archive", "Archive completed work"}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Metadata and history:")
	printCommandRows(w, [][2]string{{"state", "Inspect or set lifecycle state"}, {"label", "List, add, or remove labels"}, {"deep", "Set or clear deep-work classification"}, {"events", "Show item history"}, {"attention", "Inspect or defer attention"}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Agent and checkout:")
	printCommandRows(w, [][2]string{{"workspace", "Inspect or manage the worktree"}, {"terminal", "Inspect or manage tmux access"}, {"agent", "Inspect or control the primary agent"}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Administration:")
	printCommandRows(w, [][2]string{{"merge", "Rebase and fast-forward a target branch"}, {"delete", "Delete archived work"}, {"shutdown", "Stop wi processes and terminals"}, {"daemon", "Inspect or control the daemon"}, {"info", "Show resolved paths"}, {"version", "Show version information"}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "States:")
	fmt.Fprintln(w, "  backlog | working | waiting | archived")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Item selection:")
	fmt.Fprintln(w, "  Pass --item <selector>, an item argument, or run inside a claimed checkout.")
	fmt.Fprintln(w, "  Selectors may be IDs, active slugs, or unique active-item keywords.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global options:")
	printOptions(w, []helpOption{{"--json", "Emit structured JSON; interactive commands do not attach."}, {"-h, --help", "Show global or command-specific help."}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'wi help <command>' for command details.")
}

func printCommandRows(w io.Writer, rows [][2]string) {
	for _, row := range rows {
		fmt.Fprintf(w, "  %-18s %s\n", row[0], row[1])
	}
}

func printHelp(w io.Writer, topic string) bool {
	topic = strings.Join(strings.Fields(topic), " ")
	if topic == "" {
		printUsage(w)
		return true
	}
	doc, ok := helpDocs[topic]
	if !ok {
		return false
	}
	fmt.Fprintf(w, "wi %s - %s\n", topic, doc.Summary)
	if len(doc.Usage) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Usage:")
		for _, usage := range doc.Usage {
			fmt.Fprintf(w, "  %s\n", usage)
		}
	}
	if len(doc.Description) > 0 {
		fmt.Fprintln(w)
		for i, paragraph := range doc.Description {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, paragraph)
		}
	}
	options := append([]helpOption{}, doc.Options...)
	options = append(options,
		helpOption{"--json", "Emit structured JSON (global option)."},
		helpOption{"-h, --help", "Show this help."},
	)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	printOptions(w, options)
	if len(doc.Environment) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Environment:")
		printOptions(w, doc.Environment)
	}
	if len(doc.Examples) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Examples:")
		for _, example := range doc.Examples {
			fmt.Fprintf(w, "  %s\n", example)
		}
	}
	if len(doc.SeeAlso) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "See also:")
		for _, related := range doc.SeeAlso {
			fmt.Fprintf(w, "  wi help %s\n", related)
		}
	}
	return true
}

func printOptions(w io.Writer, options []helpOption) {
	width := 0
	for _, option := range options {
		if len(option.Flag) > width {
			width = len(option.Flag)
		}
	}
	for _, option := range options {
		fmt.Fprintf(w, "  %-*s  %s\n", width, option.Flag, option.Text)
	}
}

func requestedHelpTopic(args []string) (string, bool) {
	help := false
	literal := false
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--" {
			literal = true
			filtered = append(filtered, arg)
			continue
		}
		if !literal && (arg == "--help" || arg == "-h") {
			help = true
			continue
		}
		filtered = append(filtered, arg)
	}
	if !help {
		return "", false
	}
	if len(filtered) == 0 {
		return "", true
	}
	topic := filtered[0]
	for i := 2; i <= len(filtered); i++ {
		if filtered[i-1] == "--" {
			break
		}
		candidate := strings.Join(filtered[:i], " ")
		if _, ok := helpDocs[candidate]; ok {
			topic = candidate
		}
	}
	return topic, true
}
