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
	"init": {
		Summary:     "Set up Wi and select a default coding agent",
		Usage:       []string{"wi init [--default PROFILE]"},
		Description: []string{"Asks bundled drivers to discover installed agents on PATH, then prompts for the default profile. Writes explicit profiles with absolute executable paths to $XDG_CONFIG_HOME/wi/config.toml, or ~/.config/wi/config.toml. Never overwrites an existing configuration and does not start a daemon or agent."},
		Options:     []helpOption{{"--default PROFILE", "Select a detected profile without prompting. Required with --json."}},
		Examples:    []string{"wi init", "wi init --default pi", "wi init --default claude --json"},
	},
	"repo": {
		Summary:     "Manage named Wi-owned repositories",
		Usage:       []string{"wi repo <add|list|show|rename|archive|restore|upstream|status|sync|settings>"},
		Description: []string{"Single-repository commands default to the current item's registered repository when NAME is omitted; explicit names or IDs override context. Rename accepts only NEW for the current repository. Outside an item, pass a selector. List remains global and add requires a new name. Repository names are unique human-facing selectors. Stable repository IDs determine Git store paths and item references. Registering a repository authorizes project-local agent resources in its verified managed worktrees. Pi receives per-launch approval; direnv shell-code approval remains separate."},
		SeeAlso:     []string{"repo list", "repo rename", "repo archive", "repo restore", "repo settings"},
	},
	"repo settings":       {Summary: "Manage operator-owned repository settings", Usage: []string{"wi repo settings show [NAME] [--show-sensitive]", "wi repo settings set [NAME] plugins.PLUGIN.OPTION VALUE", "wi repo settings unset [NAME] plugins.PLUGIN.OPTION"}, Description: []string{"Repository names or IDs select stable daemon-owned settings. Omitted selectors use the current item repository. The daemon validates registered options and repository-scope permissions, fences concurrent changes by revision, and records compact change facts without setting values. These endpoints are operator-only. Rename and archive preserve settings. Changes apply to future operations, never reconfigure a running agent."}, SeeAlso: []string{"repo settings show", "repo settings set", "repo settings unset", "config plugins"}},
	"repo settings show":  {Summary: "Inspect stored overrides and effective plugin settings", Usage: []string{"wi repo settings show [NAME] [--show-sensitive]"}, Description: []string{"Includes stored overrides, effective plugin values, origins, revision, and warnings. JSON namespaces use plugins.NAME. Sensitive and unavailable values are hidden unless --show-sensitive is supplied. Does not execute plugin hooks or load a checkout."}, Options: []helpOption{{"--show-sensitive", "Reveal private stored/effective values; may expose secrets."}}, SeeAlso: []string{"repo settings"}},
	"repo settings set":   {Summary: "Set a registered repository option", Usage: []string{"wi repo settings set [NAME] plugins.PLUGIN.OPTION VALUE"}, Description: []string{"Strings are literal arguments, booleans are true/false, integers are decimal, and string arrays use JSON syntax. Global-only, unregistered, and invalid options are rejected by the daemon. Explicit false and empty values are overrides. Does not approve .envrc or start an agent as part of the command itself."}, Examples: []string{"wi repo settings set biskoui plugins.direnv.trust always", "wi repo settings set biskoui plugins.direnv.load false"}, SeeAlso: []string{"repo settings"}},
	"repo settings unset": {Summary: "Remove an override and restore inheritance", Usage: []string{"wi repo settings unset [NAME] plugins.PLUGIN.OPTION"}, Description: []string{"Removes the stored override and falls back to global user settings or plugin defaults. Also permits cleanup of preserved settings for unavailable plugins. Existing runtimes remain unchanged."}, SeeAlso: []string{"repo settings"}},
	"repo upstream": {
		Summary:     "Configure an optional remote integration upstream",
		Usage:       []string{"wi repo upstream [NAME] URL [--branch BRANCH]", "wi repo upstream [NAME] --clear"},
		Description: []string{"Atomically updates the owned Git store’s origin URL, remote-tracking fetch refspec, and integration branch tracking. Git is the source of truth. --clear removes branch tracking without deleting origin; no tracking means local-only operation. Branch defaults to the integration branch. No fetch, branch advancement, push, or original-checkout modification is performed. Finish or abort pending reconciliation before changing it."},
		Options:     []helpOption{{"--branch BRANCH", "Remote branch; defaults to the integration branch."}, {"--clear", "Make the integration branch local-only."}},
		SeeAlso:     []string{"repo status", "repo sync"},
	},
	"repo status": {
		Summary:     "Inspect integration and cached upstream history",
		Usage:       []string{"wi repo status [NAME] [--fetch]"},
		Description: []string{"Reports untracked, unfetched, equal, ahead, behind, or diverged history, local-only commits, fetch time, and pending reconciliation. Without --fetch, remote observations are cached and may be stale. --fetch refreshes the upstream but never advances the integration branch."},
		Options:     []helpOption{{"--fetch", "Refresh the upstream observation without integrating."}},
	},
	"repo sync": {
		Summary:     "Synchronize the owned integration branch without pushing",
		Usage:       []string{"wi repo sync [NAME]", "wi repo sync [NAME] --merge", "wi repo sync [NAME] --reset-to-upstream --yes", "wi repo sync [NAME] --continue", "wi repo sync [NAME] --abort"},
		Description: []string{"Defaults to the current work item’s registered repository when NAME is omitted. Outside a work item, pass NAME explicitly. Fetches the optional upstream and fast-forwards when behind; leaves an ahead branch alone and refuses divergence by default. --merge reconciles divergence in a dedicated detached checkout, preserving both histories. Resolve and stage conflicts there, then use --continue; --abort leaves integration unchanged and refuses to discard untracked files. --reset-to-upstream --yes replaces the integration tip with the fetched upstream after previewing and checking the exact tips, retaining the old tip under a recovery ref. Item branches and original checkouts remain unchanged. Active integrations, active Git operations, and checked-out integration branches are rejected. No automatic rebase or push is performed."},
		Options:     []helpOption{{"--merge", "Explicitly merge divergent histories in a reconciliation checkout."}, {"--reset-to-upstream", "Replace the local integration tip, retaining a recovery ref."}, {"--yes", "Confirm --reset-to-upstream."}, {"--continue", "Finish pending synchronization after native conflict resolution."}, {"--abort", "Abort pending reconciliation, preserving unrelated files."}},
	},
	"repo add": {
		Summary:     "Import an independent owned Git store",
		Usage:       []string{"wi repo add NAME --from SOURCE [--branch BRANCH] [--local | --upstream URL] [--upstream-branch BRANCH]"},
		Description: []string{"Creates an independent owned store without retaining the onboarding location or modifying the source checkout. URL onboarding sets upstream to that URL and the integration branch. Local onboarding uses its origin URL and origin tracking branch when configured, otherwise the integration branch name. Without an origin it stays local-only. --local leaves branch tracking unset; --upstream and --upstream-branch override the Git tracking destination. No extra upstream fetch is performed during onboarding."},
		Options:     []helpOption{{"--from SOURCE", "Local source checkout or credential-free Git URL."}, {"--branch BRANCH", "Integration branch; required for URL onboarding."}, {"--local", "Onboard without an upstream."}, {"--upstream URL", "Override the inferred integration destination."}, {"--upstream-branch BRANCH", "Override the remote branch name."}},
	},
	"repo show": {
		Summary: "Show registered repository metadata", Usage: []string{"wi repo show [NAME]"},
		Description: []string{"Shows the owned Git store, selected integration branch, current Git tracking, and archive state. Use repo status for ahead/behind observations."},
	},
	"repo list": {
		Summary:     "List registered repositories",
		Usage:       []string{"wi repo list [--all]"},
		Description: []string{"Lists active names, integration branches, state, and upstream URL/branch or untracked. Untracked means the integration branch has no upstream tracking, even if an origin remote exists. --all includes archived repositories marked archived. JSON combines registry metadata with a live Git upstream observation, marked upstream_source: git, and archived: true on archived entries."},
		Options:     []helpOption{{"--all", "Include archived repositories."}},
	},
	"repo archive": {
		Summary:     "Hide a repository and stop new item creation",
		Usage:       []string{"wi repo archive [NAME]"},
		Description: []string{"Records repository.archived and hides it from the default repository list. New items are rejected, including when inheriting the repository from the current item. The name remains reserved. Existing items and running agents remain usable and untouched. Git stores, branches, unpushed commits, dirty worktrees, and external checkouts are preserved; no cleanliness or push checks are needed. Repeating archive is a no-op. IDs may also be used as selectors. There is no repository deletion command."},
		SeeAlso:     []string{"repo restore", "repo list"},
	},
	"repo restore": {
		Summary:     "Make an archived repository available again",
		Usage:       []string{"wi repo restore [NAME]"},
		Description: []string{"Records repository.restored and allows new items again without materializing a workspace or runtime. Repeating restore on an active repository is a no-op. IDs may also be used as selectors."},
		SeeAlso:     []string{"repo archive"},
	},
	"repo rename": {
		Summary:     "Rename a registered repository",
		Usage:       []string{"wi repo rename [OLD] NEW"},
		Description: []string{"Changes the unique repository name and records repository.renamed. IDs, item references, branches, worktrees and conversations remain unchanged. The old name stops resolving. Renaming to the current name is a no-op."},
		Examples:    []string{"wi repo rename infra netfabric-infra"},
	},
	"daemon": {
		Summary:     "Run and inspect the authoritative local coordinator",
		Usage:       []string{"wi daemon <start|serve|status|stop|doctor>"},
		Description: []string{"The daemon is the authoritative per-data-root event store and the only writer of canonical state.", "Commands start it when needed and replace one of another build. With [daemon] managed = true in config.toml, a service manager owns it: wi never starts, stops or replaces it, and the daemon refuses shutdown requests."},
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
		Description: []string{"Opens the central database and serves the versioned user-only Unix socket until stopped or signaled. Service managers run this."},
	},
	"daemon status": {
		Summary:     "Inspect a running coordinator",
		Usage:       []string{"wi daemon status"},
		Description: []string{"Reports daemon identity, socket, database schema, and committed global event sequence."},
	},
	"daemon doctor": {
		Summary:     "Validate the running coordinator and native sources",
		Usage:       []string{"wi daemon doctor"},
		Description: []string{"Checks connectivity, schema metadata, and the latest native Conversation import warnings."},
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
	"config": {
		Summary:     "Inspect global configuration without starting services",
		Usage:       []string{"wi config [show] [--origins] [--show-sensitive]", "wi config get KEY [--origins] [--show-sensitive]", "wi config agents", "wi config drivers [--show-sensitive]", "wi config plugins"},
		Description: []string{"Read-only queries of global user configuration with built-in defaults. Never starts or contacts a daemon, driver, plugin, or direnv; does not evaluate project files or environment rules. Repository policy is managed by wi repo settings, not configuration files. Configuration must be valid.", "Driver-specific settings and custom driver commands are redacted by default because they can contain secrets. --show-sensitive reveals them, including in JSON. get returns the selected JSON value with --json, or a plain scalar/dotted settings in text; unknown keys exit 3."},
		Options:     []helpOption{{"--origins", "Wrap output with effective value and plugin option origins."}, {"--show-sensitive", "Reveal sensitive plugin settings, opaque driver settings, and custom commands."}},
		Examples:    []string{"wi config agents", "wi config get default_agent", "wi config get deep_work.max_active", "wi config show --json"},
		SeeAlso:     []string{"init", "info", "config show", "config get", "config agents", "config drivers", "config plugins"},
	},
	"config show":    {Summary: "Show global settings with built-in defaults", Usage: []string{"wi config show [--show-sensitive]"}, Description: []string{"Outputs sorted dotted settings in text or a configuration object with --json. Driver-specific values and commands are redacted unless --show-sensitive is supplied. Repository entity settings are separate; project files and environment rules are not evaluated."}, SeeAlso: []string{"config"}},
	"config get":     {Summary: "Query one configuration key or section", Usage: []string{"wi config get KEY [--show-sensitive]"}, Description: []string{"Use dot-separated JSON keys, such as default_agent, deep_work.max_active, item.defaults.labels, agent.startup_message, agents.pi.config, or drivers.pi.defaults. Unknown keys exit 3. Scalars print directly in text; --json always emits the selected value as JSON."}, SeeAlso: []string{"config"}},
	"config plugins": {Summary: "Inspect registered plugin option schemas", Usage: []string{"wi config plugins [--json]"}, Description: []string{"Returns option schemas from the common native/executable plugin catalog, including declarative third-party manifests. Includes defaults, types, bounds, sensitivity and repository scope. Never executes plugin code."}, SeeAlso: []string{"config"}},
	"config agents":  {Summary: "List configured agent profiles and the default", Usage: []string{"wi config agents"}, Description: []string{"Lists profiles sorted by name, with driver, optional label, and a default marker. JSON returns default_agent and an agents array containing name, driver, label when set, and default. Does not discover installations, check authentication, or inspect runtime availability."}, SeeAlso: []string{"config", "agent", "init"}},
	"config drivers": {Summary: "Inspect configured drivers and their defaults", Usage: []string{"wi config drivers [--show-sensitive]"}, Description: []string{"Queries the drivers section, including built-in drivers. Custom commands and opaque defaults are redacted unless --show-sensitive is supplied. No driver executable is resolved or launched."}, SeeAlso: []string{"config"}},
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
		Usage:   []string{"wi new --title <text> [options]", "wi new --untitled [options]"},
		Description: []string{
			"Normally creates a work item in backlog. Durable metadata and DESCRIPTION.md are written immediately; no terminal session, conversation, runtime, or composed use case is started. Items begin with an absent managed-slot checkout. Select a registered repository with --repo NAME, or inherit the current item’s repository. Without --base, the immutable starting commit comes from the registered integration branch, with no fallback to HEAD. When the repository has an upstream and repo.follow_upstream is fast-forward (the default), the branch is first fetched and fast-forwarded like wi repo sync; if that fails or the histories diverged, the item starts from the local branch with a warning. An explicit --base never fetches.",
			"With --start, the item moves to working and an unattached agent receives the global agent.startup_message through runtime control. --prompt supplies that message and implies --start; it never changes DESCRIPTION.md. --desc-file remains the durable task description. TUI mode is the default: it creates the normal tmux runtime for later entry but does not switch or attach the caller. Use --agent-mode headless for an explicitly headless runtime. If startup or delivery fails, the command reports the durable partial outcome instead of deleting the item.",
			"Use --desc-file for a backlog item's self-contained multiline description. Labels are repeatable and augment defaults from user config, <repository>/.config/wi.toml, and WI_ITEM_DEFAULT_LABELS; use --no-default-labels to opt out. Deep work is a built-in attribute, not a label.",
			"A sandbox may set WI_AGENT_DAEMON_SOCKET to the daemon's separately mounted agent endpoint. That endpoint permits only absent managed-slot backlog creation; --start is rejected. Worktree-relative bases require item and repository context available through the endpoint.",
		},
		Options: []helpOption{
			{"--title <text>", "Explicit title; required unless --untitled is supplied. Derive the slug unless --slug is supplied; no title inference."},
			{"--untitled", "Use the editable title Untitled task and an item-<ID suffix> slug unless --slug is supplied. Cannot be combined with --title."},
			{"--desc-file <path>", "Read DESCRIPTION.md content from a file."},
			{"--start", "Start an unattached agent and send the configured startup message."},
			{"--prompt <text>", "Start an agent and send this message without changing the description; implies --start."},
			{"--agent <name>", "Select a named agent profile; omitted uses default_agent from config."},
			{"--model <model>", "Choose the item's model, overriding the profile's. Stored as given; the driver checks it when the agent starts."},
			{"--reasoning <level>", "Choose the item's reasoning level: off, minimal, low, medium, high, xhigh, or max. Drivers and models accept a subset."},
			{"--agent-mode <mode>", "With --start or --prompt, start tui (default) or headless."},
			{"--slug <slug>", "Set the initial active slug; must be unique."},
			{"--label <label>", "Add a label after inherited defaults; repeatable."},
			{"--no-default-labels", "Do not inherit user or repository default labels."},
			{"--deep", "Mark the item as deep work."},
			{"--repo <name>", "Select a registered repository; overrides the current item’s repository."},
			{"--base <revision>", "Use a named branch, tag, or commit from the owned store. ^ selects integration. HEAD, @, - and contextual expressions resolve in the current claimed worktree, or fail if context is missing or the repository differs."},
		},
		Environment: []helpOption{{"WI_ITEM_DEFAULT_LABELS", "Comma-separated additive default labels."}, {"WI_AGENT_DAEMON_SOCKET", "Use the restricted agent endpoint at an explicit absolute path; it is never auto-started."}},
		Examples: []string{
			`wi new --label follow-up --desc-file handoff.md --title "Investigate refresh race"`,
			`wi new --desc-file handoff.md --start --title "Investigate refresh race"`,
			`wi new --desc-file audit.md --start --agent-mode headless --prompt "Read wi show and run the audit." --title "Audit dependencies"`,
			`wi new --deep --repo api --title "Redesign authentication"`,
			`wi new --model opus --reasoning xhigh --desc-file plan.md --start --agent claude --title "Plan the migration"`,
			`wi new --base=@ --title "Build on this task"`,
			`wi new --base=^ --title "Independent task"`,
			`wi new --untitled --repo api`,
		},
	},
	"list": {
		Summary: "List and filter work items",
		Usage:   []string{"wi list [options]"},
		Description: []string{
			"JSON preserves lifecycle-keyed sections and adds display_sections with key, title, and ordered item_ids matching text grouping. Full IDs and slugs remain available in sections. Empty display groups have empty arrays. JSON is an object, not an item array.",
			"Shows open work grouped by actionability. Closed items are hidden by default. --closed selects closed-only, --all includes open and closed, and --state closed implies closed visibility. Live agent/worktree inspection adds AGENT and WORKTREE columns unless --no-agent is used.",
			"Active items with an open blocker appear under WAITING, outside the attention ranking, unless the agent responded since the item was last read: that unread response puts the item back in NEEDS ATTENTION until it is read. Unread titles are bold (or marked unread without color). What an item waits for follows its name in yellow (a blocker's label such as #1234, or a compact form like until Fri 09:00 or on api-schema), and closed items show their RESOLUTION.",
			"Label rules are additive. A bare label or +label requires that label; -label excludes it. All positive labels must match and no negative label may match.",
			"Global defaults use labels = [\"+team\", \"-personal\"] in the [list] section of config.toml. WI_LIST_LABELS accepts comma-separated rules. CLI rules use repeatable --label flags.",
			"Rules merge by source precedence: config list.labels < WI_LIST_LABELS < CLI --label. A higher-precedence rule replaces only the same normalized label while unrelated inherited rules remain. Within one source, rules are processed left-to-right and the later rule wins for the same label. The token ! clears all inherited rules before subsequent tokens are applied.",
			"Within NEEDS ATTENTION, [attention] priority selects the ranking strategy. The current recent-request strategy orders non-deferred items by newest observed user prompt, then deferred items by oldest attention.deferred event. JSON exposes the selected strategy and requested, completed, and deferred timestamps.",
		},
		Options: []helpOption{
			{"--label <rule>", "Apply a label rule; repeatable. Forms: label, +label, -label, !."},
			{"--state <state>", "Filter by backlog, active, or closed."},
			{"--closed", "Show closed items only."},
			{"--all", "Show open and closed items."},
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
	"edit": {
		Summary: "Edit work item metadata together",
		Usage:   []string{"wi edit [--item <selector>] [options] [item]"},
		Description: []string{
			"Edits title, description, labels, and deep-work classification in one validated request. With no edit flags, opens title and description in VISUAL, EDITOR, or vi. Defaults to the current item; closed items can be selected by ID. State, slug, branch, checkout, conversation, and runtime remain unchanged; no prompt is sent.",
			"The editor uses a private draft with a Title: line, a blank line, then description text. Concurrent item or description changes refuse the edit rather than overwriting them; failed drafts are retained. Description content stays on disk and is staged for crash recovery. A post-commit filesystem failure reports the retained edit and pending description instead of claiming rollback.",
		},
		Options: []helpOption{
			{"--item <selector>", "Select an item; defaults to the current item."},
			{"--title <text>", "Replace the non-empty, single-line UTF-8 title; at most 512 bytes."},
			{"--desc-file <path>", "Replace the UTF-8 description from a file; at most 4 MiB. An empty file clears it."},
			{"--label <label>", "Add a label; repeatable."},
			{"--remove-label <label>", "Remove a label; repeatable. Cannot also add the same label."},
			{"--deep", "Mark as deep work."},
			{"--no-deep", "Clear deep work; cannot be combined with --deep."},
		},
		Examples: []string{`wi edit --title "Clarified objective"`, `wi edit --item auth --desc-file task.md --label backend`, `wi edit --no-deep auth`, `wi edit auth`},
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
		Description: []string{"Reads backlog, active, or closed without inspecting workspace or agent resources."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"state set": {
		Summary:     "Set durable lifecycle state without workspace effects",
		Usage:       []string{"wi state set <backlog|active|closed> [--item <selector>] [--force] [item]"},
		Description: []string{"Performs a legal state transition and appends a durable event. Workspace resources are deliberately left unchanged. Setting closed records the completed resolution and clears the active slug; setting a closed item to backlog allocates a fresh slug and clears its resolution. "},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--force", "Override deep-work capacity only when setting backlog to active."},
		},
		Examples: []string{`wi state set active --item auth`, `wi state set backlog auth`},
	},
	"delete": {
		Summary:     "Irreversibly delete closed work-item case files",
		Usage:       []string{"wi delete --yes [--item <selector>] [item]", "wi delete --closed --yes"},
		Description: []string{"Deletes the complete work-item case file, including its description and conversations. Deletion requires closed state and refuses live agent runtimes, tmux terminals, or materialized checkouts. --closed validates every closed item before deleting any of them."},
		Options: []helpOption{
			{"--item <selector>", "Select one closed item explicitly."},
			{"--closed", "Delete all closed items after validating the complete set."},
			{"--yes", "Confirm irreversible deletion."},
		},
		Examples: []string{`wi delete --yes --item 01KZ39T`, `wi delete --closed --yes`},
	},
	"attention": {
		Summary:     "Inspect attention activity, ranking, deferral, and read state",
		Usage:       []string{"wi attention <activity|defer|queue|read|unread> [options]"},
		Description: []string{"Attention facts and ranking are independent of lifecycle state mutation and workspace navigation."},
		SeeAlso:     []string{"attention activity", "attention defer", "attention queue", "attention read", "attention unread", "next"},
	},
	"attention activity": {
		Summary:     "Show prompt, completion, and defer timestamps",
		Usage:       []string{"wi attention activity [--item <selector>] [item]"},
		Description: []string{"Shows the latest facts used to rank attention. Timestamps are derived from native conversation history and the work-item event log."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"attention defer": {
		Summary:     "Move a NEEDS ATTENTION item to the back of the queue",
		Usage:       []string{"wi attention defer [--item <selector>] [item]"},
		Description: []string{"Records attention.deferred for an active item without inspecting agent or workspace eligibility. It never changes lifecycle state or workspace resources. Most interactive navigation should use wi next --defer, which conditionally defers only a current NEEDS ATTENTION item."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"attention read": {
		Summary:     "Mark the agent's responses read",
		Usage:       []string{"wi attention read [--item <selector>] [item]"},
		Description: []string{"Records attention.read. Reading never moves an item that is not waiting; a waiting item with an unread response leaves NEEDS ATTENTION once read. wi switch, the picker, wi next, and opening a web conversation mark the item read."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"attention unread": {
		Summary:     "Mark the latest response unread again",
		Usage:       []string{"wi attention unread [--item <selector>] [item]"},
		Description: []string{"Records attention.unread, for example after opening a conversation without reading it. A waiting item returns to NEEDS ATTENTION."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"wait": {
		Summary: "Make an item wait for a time, another item, or something external",
		Usage:   []string{"wi wait [--until WHEN] [--on ITEM] [--for TEXT] [--item <selector>] [item]", "wi wait --clear [--item <selector>] [item]"},
		Description: []string{
			"Adds one blocker per condition to a backlog or active item, in one atomic step; combining conditions waits for all of them. Lifecycle state and resources are unchanged; an active waiting item leaves the attention queue and is listed under WAITING with what it waits for.",
			"A time blocker is a snooze: the daemon clears it when the time passes or when you next prompt the item, since prompting means you decided to work on it. Item blockers are cleared when the other item closes; each clearance is recorded in the item's history with its cause. --for events stay until cleared, by you or by automation that tagged them with metadata. --clear stops waiting by clearing every blocker. For structured metadata or clearing one blocker, use wi blocker.",
			"Waiting marks the item's current responses read; a later unread response puts it back in NEEDS ATTENTION until it is read.",
		},
		Options: []helpOption{
			{"--until <when>", "Snooze until a time: a duration such as 90m, 4h, or 2d; tomorrow or a weekday such as monday (the next one at 09:00); or a local date/time such as 2026-10-20 or 2026-10-20T09:00."},
			{"--on <item>", "Wait until another work item closes; - chooses it in the picker. Dependency cycles are refused."},
			{"--for <event>", "Wait for an external event, named by the text."},
			{"--clear", "Clear every blocker."},
			{"--item <selector>", "Select an item explicitly."},
		},
		Examples: []string{`wi wait --until 1d`, `wi wait --until monday`, `wi wait --on api-schema`, `wi wait --on -`, `wi wait --until 3d --on api-schema`, `wi wait --for "security review"`, `wi wait --clear`},
	},
	"blocker": {
		Summary:     "Inspect and edit an item's blockers directly",
		Usage:       []string{"wi blocker <add|list|clear> [options]"},
		Description: []string{"This primitive operates on the blocker list behind wi wait. Each blocker has an ID (shown by wi blocker list), a kind with its condition (a time, another item, or an external event name), and optional structured key=value metadata that automation can match before clearing it."},
		SeeAlso:     []string{"blocker add", "blocker list", "blocker clear", "wait"},
	},
	"blocker add": {
		Summary: "Add one typed blocker",
		Usage:   []string{"wi blocker add --kind <time|item|external> (--until WHEN | --on ITEM | --event NAME) [--label TEXT] [--meta key=value]... [--item <selector>] [item]"},
		Options: []helpOption{
			{"--kind <kind>", "time requires --until; item requires --on; external requires --event."},
			{"--until <when>", "A duration or local date/time."},
			{"--on <item>", "The work item that must close first."},
			{"--event <name>", "The external event the item waits for."},
			{"--label <text>", "Short display form for lists, such as #1234; up to 40 characters."},
			{"--meta <key=value>", "Structured metadata; keys are lowercase letters, digits, '.', '_', or '-'; repeatable, up to 16."},
			{"--item <selector>", "Select an item explicitly."},
		},
		Examples: []string{`wi blocker add --kind external --event "security review" --meta issue=SEC-12`, `wi blocker add --kind external --event "API review" --label "API #42" --meta ticket=API-42`},
	},
	"blocker list": {
		Summary: "List an item's blockers",
		Usage:   []string{"wi blocker list [--item <selector>] [item]"},
		Options: []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"blocker clear": {
		Summary: "Clear one blocker or all of them",
		Usage:   []string{"wi blocker clear (--id ID | --all) [--item <selector>] [item]"},
		Options: []helpOption{
			{"--id <id>", "Clear one blocker."},
			{"--all", "Clear every blocker."},
			{"--item <selector>", "Select an item explicitly."},
		},
	},
	"attention queue": {
		Summary:     "Show the ranked NEEDS ATTENTION queue",
		Usage:       []string{"wi attention queue [--label <rule>]..."},
		Description: []string{"Returns candidates ranked by the configured attention.priority strategy without choosing a successor or entering a workspace."},
		Options:     []helpOption{{"--label <rule>", "Apply the same repeatable label rules as wi list."}},
		Environment: []helpOption{{"WI_LIST_LABELS", "Comma-separated label rules overlaid on config."}},
	},
	"note": {
		Summary: "Add a note to an item's log",
		Usage:   []string{"wi note [--item <selector>] <text>"},
		Description: []string{
			"Appends a short, timestamped note to the item's log: a decision, an abandoned approach, a blocker, a milestone. Notes are append-only and at most 2 KB; attach longer content instead. When the item has a worktree, the note records its HEAD commit.",
			"Notes are quiet: they never count as unread, never raise attention, and are never sent to the agent. The agent endpoint may add them.",
		},
		Options:  []helpOption{{"--item <selector>", "Select an item; defaults to the current item. Put options before the text."}},
		Examples: []string{`wi note "Dropped the cache approach: invalidation races with merge"`, `wi note --item auth "Waiting on the API team's answer"`},
		SeeAlso:  []string{"notes", "attach"},
	},
	"search": {
		Summary: "Find work items by text",
		Usage:   []string{"wi search [--state <state>] [--limit <n>] <query>"},
		Description: []string{
			"Finds items whose title, slug, labels, description, or notes contain every word of the query, ignoring case. Quote a phrase to match it as one term. Attachment names count as notes. Results come first by the strongest field matched (title, labels, description, then notes), then by most recent update; a description or notes match shows an excerpt around it. Closed items are included unless --state narrows the search.",
		},
		Options:  []helpOption{{"--state <state>", "Only backlog, active, or closed items."}, {"--limit <n>", "Show at most n results (default 50)."}},
		Examples: []string{`wi search cache`, `wi search --state closed "session cache"`, `wi --json search otto deploy`},
		SeeAlso:  []string{"list", "notes"},
	},
	"notes": {
		Summary:     "Show an item's notes",
		Usage:       []string{"wi notes [--item <selector>] [item]"},
		Description: []string{"Prints the item's notes, oldest first, with the worktree commit each was taken at. Attaching a file adds a note too. JSON output returns the full records."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
		Examples:    []string{`wi notes`, `wi --json notes auth`},
		SeeAlso:     []string{"note", "attachments"},
	},
	"attach": {
		Summary: "Attach a file to an item",
		Usage:   []string{"wi attach [--item <selector>] [--as <name>] [-m <note>] <file>"},
		Description: []string{
			"Copies a file into the item, outside Git: a report, a screenshot, evidence. The original is left alone. Attaching under an existing name replaces it; earlier content stays readable by the digest its note recorded. Each attach adds a note. Files are at most 16 MB. The agent endpoint may attach files.",
		},
		Options: []helpOption{
			{"--item <selector>", "Select an item; defaults to the current item."},
			{"--as <name>", "Name the attachment; defaults to the file's name."},
			{"-m <note>", "Record this note instead of \"Attached <name>\"."},
		},
		Examples: []string{`wi attach REPORT.md`, `wi attach --as login-bug.png -m "Login fails after redirect" /tmp/shot.png`},
		SeeAlso:  []string{"attachments", "attachment cat", "notes"},
	},
	"attachments": {
		Summary:     "List an item's attachments",
		Usage:       []string{"wi attachments [--item <selector>] [item]"},
		Description: []string{"Lists each attachment name with its latest content's size and time."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
		Examples:    []string{`wi attachments`, `wi --json attachments auth`},
		SeeAlso:     []string{"attach", "attachment cat"},
	},
	"attachment": {
		Summary:  "Read an item's attachments",
		Usage:    []string{"wi attachment cat [--item <selector>] <name|digest>"},
		Examples: []string{`wi attachment cat REPORT.md`},
		SeeAlso:  []string{"attachment cat", "attachments"},
	},
	"attachment cat": {
		Summary:     "Print an attachment",
		Usage:       []string{"wi attachment cat [--item <selector>] <name|digest>"},
		Description: []string{"Prints the latest content attached under a name, or earlier content by the digest a note recorded."},
		Options:     []helpOption{{"--item <selector>", "Select an item; defaults to the current item."}},
		Examples:    []string{`wi attachment cat REPORT.md`, `wi attachment cat --item auth login-bug.png > bug.png`},
		SeeAlso:     []string{"attachments", "notes"},
	},
	"events": {
		Summary:     "Show the durable work-item event log",
		Usage:       []string{"wi events [--item <selector>] [item]"},
		Description: []string{"Prints the compact append-only daemon event journal, including lifecycle, workspace, merge, runtime, and attention.deferred records. JSON output returns structured events."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
		Examples:    []string{`wi events`, `wi events --item auth`, `wi --json events auth`},
	},
	"merge": {
		Summary: "Fast-forward a local target branch, rebasing when needed",
		Usage:   []string{"wi merge [--item <selector>] [--target <branch>] [--local] [target]"},
		Description: []string{
			"Merges the current active item without switching checkouts: integrates committed work while preserving uncommitted source changes, rebases only when needed using a temporary checkout for dirty sources, atomically fast-forwards the local target ref, and synchronizes any worktree where the target is checked out.",
			"The target defaults to the registered integration branch. With a configured upstream, wi fetches and fast-forward-synchronizes that base before integrating the item; divergence stops before rebasing. --local skips that remote preflight. Other explicitly selected target branches remain local-only. No push, squash, branch deletion, workspace cleanup, shelve, or close is performed.",
			"If item rebase, target advancement, or target-worktree synchronization fails, wi aborts/rolls back the item integration. An earlier successful upstream synchronization is deliberately retained and reported. Dirty non-integration target files are allowed only when they do not overlap incoming paths.",
		},
		Options: []helpOption{
			{"--item <selector>", "Select the source work item explicitly."},
			{"--target <branch>", "Select an existing local target branch instead of positional target."},
			{"--local", "Skip upstream network access and base synchronization; pending reconciliation still blocks merging."},
		},
		Examples: []string{`wi merge`, `wi merge main`, `wi merge --item auth --target main`, `wi merge --target develop`, `wi --json merge main`},
	},
	"start": {
		Summary: "Start an existing backlog item",
		Usage:   []string{"wi start [--agent-mode <tui|headless>] [--model <model>] [--reasoning <level>] [--force] <slug>"},
		Description: []string{
			"Resolves an exact active slug or one unambiguous active-slug substring. Exact matches take precedence; ambiguous substrings fail with candidate IDs and slugs. IDs, title keywords, implicit current-item selection, and --item are not accepted.",
			"Transitions backlog -> active, enforces deep-work capacity, materializes the worktree, and ensures the selected primary runtime. TUI mode attaches terminal access; headless mode remains tmux-independent. JSON never attaches. Starting an existing item does not send a message.",
			"To create and start an item, use wi new --title TEXT --start.",
		},
		Options: []helpOption{
			{"--agent-mode <tui|headless>", "Start native TUI (default) or headless mode."},
			{"--model <model>", "Save the item's model before starting, as wi agent settings set model=MODEL does."},
			{"--reasoning <level>", "Save the item's reasoning level before starting."},
			{"--force", "Override the deep-work active limit."},
		},
		Examples: []string{`wi start auth-race`, `wi start --agent-mode headless audit`, `wi start --reasoning high auth-race`},
	},
	"switch": {
		Summary: "Select and switch to a work item",
		Usage: []string{
			"wi switch [--no-agent] [--no-preview] [--label <rule>]...",
			"wi switch [--take] [--item <selector>] <item>",
			"wi switch [--take] --current",
		},
		Description: []string{
			"Without an item, opens an interactive fzf picker over active and backlog items using the same actionability projection and label rules as wi list. Backlog items appear last in a distinct color. The picker initially selects the current item when present; changing the filter resets the cursor to the first match. Rows show status, title, then repository. Status and repository widths use the longest values in the full snapshot and stay fixed while filtering. The title uses the remaining list-pane width and truncates at the end, adapting on resize or preview toggles. Full titles remain searchable; slugs are available in the details preview. The preview uses 40% of the width on wide terminals and moves below the list on narrow terminals; Alt-P hides or shows it. Selecting a backlog item starts it; selecting an active item is state-neutral before entering its TUI, including a waiting one. The daemon refreshes native activity before capturing candidates; the open picker is an explicitly marked frozen snapshot and can be reopened to refresh. A tmux key binding can host the complete operation in a popup so startup prompts and errors remain visible.",
			"With an explicit item, or with --current, performs the existing state-neutral switch directly: enter a verified existing TUI terminal even when its checkout needs repair, otherwise ensure the worktree, primary TUI runtime, and tmux access, then attach or switch the client. JSON requires an explicit item or --current and never attaches.",
			"Each agent has one controller and any number of observers. Switching takes control when nobody else holds it, replacing an idle headless runtime with a TUI on the same conversation. When a web client controls the agent, switch enters the TUI read-only: tmux pane input stays disabled. --take displaces the other controller, aborting a turn in flight and restarting a headless runtime as a TUI. Switching marks the conversation read.",
		},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly instead of opening the picker."},
			{"--current", "Use implicit WI_ID, tmux, or checkout resolution instead of opening the picker."},
			{"--take", "Take control from another client; aborts a turn in flight and replaces a headless runtime."},
			{"--no-agent", "In picker mode, skip live agent/worktree inspection."},
			{"--no-preview", "In picker mode, disable the durable-details and observed-status preview pane."},
			{"--label <rule>", "In picker mode, apply the same repeatable label rules as wi list."},
		},
		Environment: []helpOption{
			{"WI_FZF", "Override the fzf-compatible picker executable."},
			{"WI_LIST_LABELS", "Comma-separated label rules overlaid on config."},
		},
		Examples: []string{`wi switch`, `wi switch agent-context`, `wi switch --current`, `wi switch --take auth`, `wi switch --label +backend --label -blocked`},
	},
	"workspace": {
		Summary:     "Inspect and manage the repository worktree",
		Usage:       []string{"wi workspace <status|ensure|release> [options] [item]"},
		Description: []string{"This core area owns only repository checkout claims and materialization. Tmux access and agent runtimes are separate resources."},
		SeeAlso:     []string{"workspace status", "workspace ensure", "workspace release", "terminal", "agent runtime"},
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
		Description: []string{"Ensures only the checkout/worktree. It does not create tmux or a conversation. Lifecycle state is unchanged."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"workspace release": {
		Summary:     "Release a clean repository worktree",
		Usage:       []string{"wi workspace release [--item <selector>] [item]"},
		Description: []string{"Releases only the workspace claim. Managed slots are retained for reuse. Active agent runtimes and terminal sessions must be stopped explicitly first."},
		Options:     []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"terminal": {
		Summary:     "Manage optional terminal access",
		Usage:       []string{"wi terminal <status|ensure|enter|close> [options] [item]"},
		Description: []string{"Low-level tmux adapter plumbing operates over an existing worktree. It never creates a conversation or changes lifecycle state."},
		SeeAlso:     []string{"terminal status", "terminal ensure", "terminal enter", "terminal close", "workspace"},
	},
	"terminal status": {Summary: "Inspect optional terminal access", Usage: []string{"wi terminal status [--item <selector>] [item]"}, Options: []helpOption{{"--item <selector>", "Select an item explicitly."}}},
	"terminal ensure": {Summary: "Ensure optional terminal access", Usage: []string{"wi terminal ensure [--item <selector>] [item]"}, Description: []string{"Requires an existing worktree and creates no agent resources."}, Options: []helpOption{{"--item <selector>", "Select an item explicitly."}}},
	"terminal enter":  {Summary: "Enter optional terminal access", Usage: []string{"wi terminal enter [--item <selector>] [item]"}, Description: []string{"Ensures tmux over an existing worktree and attaches unless JSON output is selected."}, Options: []helpOption{{"--item <selector>", "Select an item explicitly."}}},
	"terminal close":  {Summary: "Close optional terminal access", Usage: []string{"wi terminal close [--item <selector>] [item]"}, Description: []string{"Closes tmux only. A TUI agent runtime must be stopped explicitly first."}, Options: []helpOption{{"--item <selector>", "Select an item explicitly."}}},
	"next": {
		Summary: "Switch to the next item in the NEEDS ATTENTION ring",
		Usage:   []string{"wi next [--defer | --wait (--until WHEN | --on ITEM | --for TEXT)... | --close [--resolution R]] [--label <rule>]..."},
		Description: []string{
			"Builds the ranked NEEDS ATTENTION queue using the same config, WI_LIST_LABELS, and optional --label rules as wi list. If the current item is in that queue, selects its successor and wraps from the last item to the first. If the current item is busy, waiting, needs fixing, backlog, closed, or otherwise outside the queue, starts at the first NEEDS ATTENTION item.",
			"Plain navigation takes one synchronized daemon actionability snapshot, remains state-neutral, and performs the same TUI composition as wi switch. By default it records no defer, selection event, cursor, or ranking timestamp. With --defer, it first records attention.deferred only when the current item belongs to NEEDS ATTENTION; from busy, waiting, fixing, or other outside items it simply navigates.",
			"With --wait, wi atomically adds the same blockers as wi wait to the current item (at least one of --until, --on, or --for is required) and ranks the resulting daemon actionability snapshot, then navigates from the top. With --close, wi validates clean-worktree safety and stops the previous runtime before switching, then durably releases its workspace claim and closes it with --resolution (completed by default) before scheduling the previous tmux session for closure. --wait, --defer, and --close are mutually exclusive. If switching succeeds but closing fails, the new item remains selected and the error reports the partial outcome.",
			"JSON mode selects and ensures the target workspace without attaching to tmux. Therefore --json next --close invoked inside the previous item's tmux session refuses terminal cleanup rather than pretending the client switched.",
		},
		Options: []helpOption{
			{"--defer", "Defer the current item if it is in NEEDS ATTENTION before navigating."},
			{"--wait", "Make the current item wait before navigating; requires --until, --on, or --for like wi wait."},
			{"--close", "Enter a distinct successor, then stop, clean up, and close the previous item."},
			{"--resolution <name>", "With --close: completed (default), abandoned, or superseded."},
			{"--label <rule>", "Apply the same repeatable additive label rules as wi list."},
		},
		Environment: []helpOption{{"WI_LIST_LABELS", "Comma-separated label rules overlaid on config."}},
		Examples:    []string{`wi next`, `wi next --defer`, `wi next --wait --until tomorrow`, `wi next --wait --on -`, `wi next --close --resolution abandoned`, `wi next --label +backend --label -blocked`, `wi --json next --wait --until 4h`},
	},

	"resume": {
		Summary:     "Ensure an active item's agent runtime",
		Usage:       []string{"wi resume [--item <selector>] [--agent-mode <tui|headless>] [item]"},
		Description: []string{"Ensures the selected primary-agent runtime of an active item like wi start, without changing lifecycle state or waiting. Backlog items must use wi start. JSON output never attaches."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--agent-mode <tui|headless>", "Start native TUI (default) or headless Headless mode."},
		},
		Examples: []string{`wi resume`, `wi resume --item auth`},
	},
	"shelve": {
		Summary:     "Return active work to backlog",
		Usage:       []string{"wi shelve [--item <selector>] [--force] [item]"},
		Description: []string{"Transitions active -> backlog after stopping an idle agent and cleaning up its terminal and worktree, like close. A busy agent refuses unless --force aborts its turn. Blockers are kept; dirty or branch-mismatched worktrees remain protected."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--force", "Abort a busy agent, and allow the state transition while retaining an unclosable current terminal."},
		},
	},
	"close": {
		Summary: "Close a work item with a resolution",
		Usage:   []string{"wi close [--resolution completed|abandoned|superseded] [--item <selector>] [--force] [--commit-changes] [item]"},
		Description: []string{
			"Gracefully stops an idle agent runtime, waits for it to exit, closes terminal access, releases the clean workspace claim, then closes the item, records its resolution, clears its blockers, and clears its active slug. Whether the work was completed is the resolution, independent of closing. Busy runtimes require --force, which aborts before shutdown. Items waiting on this one stop waiting on it; the daemon records this item's resolution in their history.",
			"To reopen a closed item, run `wi state set backlog --item <item-id>`. Reopening retains its ID and history, assigns a fresh active slug, clears the resolution, and does not recreate workspace or agent resources.",
		},
		Options: []helpOption{
			{"--resolution <name>", "completed (default), abandoned, or superseded."},
			{"--item <selector>", "Select an item explicitly."},
			{"--force", "Abort a busy runtime before shutdown and allow closing while retaining an unclosable current terminal."},
			{"--commit-changes", "Instead of refusing a checkout with uncommitted changes, commit them to the item's branch after the agent stops. Reopening the item finds them there."},
		},
		Examples: []string{`wi close add-api-retries`, `wi close --resolution abandoned spike`, `wi state set backlog --item 01KZ39T`},
	},
	"pick": {
		Summary:     "Choose an item in the picker and print its ID",
		Usage:       []string{"wi pick [--exclude-current] [--no-agent] [--no-preview] [--label <rule>]..."},
		Description: []string{"Opens the same fzf picker as wi switch, without switching or starting anything, and prints the chosen item's ID for scripts and key bindings. JSON output adds the slug and title. Cancelling exits with an error."},
		Options: []helpOption{
			{"--exclude-current", "Leave out the current item."},
			{"--no-agent", "Skip live agent/worktree inspection."},
			{"--no-preview", "Disable the details preview."},
			{"--label <rule>", "Apply the same repeatable label rules as wi list."},
		},
		Examples: []string{`wi wait --on "$(wi pick --exclude-current)"`},
	},
	"plugin": {
		Summary: "List and run optional plugins",
		Usage:   []string{"wi plugin list [--show-sensitive]", "wi plugin run <name>", "wi plugin skill <name>"},
		Description: []string{
			"Native preparation hooks and executable plugins share one catalog and option registry. Each [plugins.NAME] table holds installation fields (enabled, an optional command override, and a third-party manifest path) next to the plugin's registered options, which supply global defaults; wi repo settings stores repository overrides as plugins.NAME.OPTION. Plugins are fully trusted operator code.",
			"Only declared commands are dispatched, without shadowing built-ins. Jobs resolve fresh repository settings and use normal Wi CLI/API calls. GitHub checks external blockers tagged github.pr; its interval_seconds defaults to 300, bounded to 30 through 86400, and ignore_authors is repository-scoped.",
			"wi plugin skill reads bounded static assets without launching code. wi plugin list includes native and executable entries, redacts sensitive values and executable commands, and accepts --show-sensitive. wi plugin run executes declared jobs even while disabled, but is rejected on the restricted agent endpoint. Explicit endpoint routing is preserved.",
		},
		Examples: []string{`wi plugin list`, `wi plugin run github`, `wi plugin skill github`, `wi github wait https://github.com/acme/api/pull/42`, `wi github wait acme/api#42 --item auth`},
	},
	"agent": {
		Summary:     "Inspect, run, monitor, and communicate with the primary agent",
		Usage:       []string{"wi agent <status|control|runtime|monitor|settings> [options]"},
		Description: []string{"Agent commands derive status from runtime events and expose direct runtime control."},
		SeeAlso:     []string{"agent status", "agent control", "agent runtime", "agent monitor", "agent settings"},
	},
	"agent settings": {
		Summary: "Show or change an item's model and reasoning level",
		Usage:   []string{"wi agent settings [show] [--item <selector>]", "wi agent settings set [--item <selector>] [--restart] NAME=VALUE..."},
		Description: []string{
			"An item sets two things: model, and reasoning, one of off, minimal, low, medium, high, xhigh, or max. Each driver maps reasoning onto its backend (Pi's thinking level, Claude's effort) and accepts a subset of levels. An item's own values override its profile for every runtime that starts or resumes its conversation; an empty VALUE resets one to the profile.",
			"show lists the effective values, where each comes from, the driver's available models and reasoning levels, and which settings the running agent can change live.",
			"set validates the settings with the driver and saves them. A running agent changes them live when its runtime supports it (Pi in both modes, Claude headless); otherwise they apply when it next starts, or now with --restart, which resumes the same conversation in the same mode. A busy agent is not restarted. Changing a running agent needs control, like sending a message.",
		},
		Options:  []helpOption{{"--item <selector>", "Select an item explicitly."}, {"--restart", "Restart a runtime that cannot apply a change live."}},
		Examples: []string{`wi agent settings`, `wi agent settings set model=openai-codex/gpt-6-sol reasoning=high`, `wi agent settings set --item auth --restart model=sonnet reasoning=xhigh`, `wi agent settings set reasoning=`},
	},
	"agent status": {
		Summary:     "Inspect derived agent and worktree status",
		Usage:       []string{"wi agent status [--item <selector>] [--stale-after <duration>] [item]", "wi agent status --all [--stale-after <duration>]"},
		Description: []string{"Shows busy, idle, or problem for one selected primary agent, separately from absent, clean, changed, or problem worktree status. --all explicitly returns the active-item overview, keeping JSON result shapes predictable."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--all", "Show the active-item agent overview instead of one item."},
			{"--stale-after <duration>", "Treat old incomplete activity as a problem (default 10m)."},
		},
	},
	"agent control": {
		Summary:     "Submit a low-level command to the owning agent runtime",
		Usage:       []string{"wi agent control send [--follow-up] [--item <selector>] [--actor <name>] [--image <path>] [--stdin | --file <path> | <message...>]", "wi agent control <abort|shutdown> [--item <selector>] [--actor <name>]"},
		Description: []string{"Submits directly to runtime control without consulting derived busy/idle status. Send defaults to the driver's steering delivery; --follow-up waits until the current run settles."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--actor <name>", "Record the control source/actor."},
			{"--stdin", "Read the message from standard input."},
			{"--file <path>", "Read the message from a file."},
			{"--image <path>", "Attach an image; repeatable, up to four files and 8 MiB total. Text is optional with images."},
		},
		Examples: []string{`wi agent control send --item auth "Focus on the failing test"`, `wi agent control send --follow-up --item auth "Summarize when finished"`},
	},
	"agent control send": {
		Summary:     "Send input with explicit delivery semantics",
		Usage:       []string{"wi agent control send [--follow-up] [--item <selector>] [--actor <name>] [--image <path>] [--stdin | --file <path> | <message...>]"},
		Description: []string{"Defaults to the driver's steering delivery; while idle, start a turn. --follow-up delivers only after the current run fully settles. Image attachments require an image-capable runtime."},
		Options:     []helpOption{{"--follow-up", "Wait until the current run fully settles."}, {"--item <selector>", "Select an item explicitly."}, {"--actor <name>", "Record the control source/actor."}, {"--stdin", "Read the message from standard input."}, {"--file <path>", "Read the message from a file."}, {"--image <path>", "Attach an image; repeatable, up to four files and 8 MiB total. Text is optional with images."}},
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
		Description: []string{"A work item has one durable primary conversation and at most one active runtime owner. Drivers advertise TUI and headless capabilities; every runtime holds the same primary-conversation lock."},
		SeeAlso:     []string{"agent runtime status", "agent runtime ensure", "agent runtime stop", "agent monitor"},
	},
	"agent runtime status": {
		Summary: "Inspect runtime mode, process ownership, and capabilities",
		Usage:   []string{"wi agent runtime status [--item <selector>] [item]"},
		Options: []helpOption{{"--item <selector>", "Select an item explicitly."}},
	},
	"agent runtime ensure": {
		Summary:     "Ensure a TUI or headless headless runtime",
		Usage:       []string{"wi agent runtime ensure --mode <tui|headless> [--item <selector>] [item]"},
		Description: []string{"TUI mode runs native inside tmux. Headless mode runs headlessly without creating a tmux session. A different live mode must be stopped before handoff."},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--mode <tui|headless>", "Select native TUI or headless Headless mode."},
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
		Summary: "Read compact runtime events, or follow live content",
		Usage:   []string{"wi agent monitor [--item <selector>] [--limit <n>] [--follow] [item]", "wi agent monitor --content [--item <selector>] [item]"},
		Description: []string{
			"Shows the compact lifecycle, message, command, and tool events retained by the daemon.",
			"--content instead follows the running agent's live content as it happens: message text as it streams, reasoning the agent shares, tool calls, activity, and which history entries a finished message was saved as. Nothing of it is retained. It waits while no agent runs and follows the next one; interrupt it to stop. With --json it prints one compact event per line.",
		},
		Options: []helpOption{
			{"--item <selector>", "Select an item explicitly."},
			{"--limit <n>", "Show the latest N events; zero means all."},
			{"--follow", "Continue streaming new normalized events."},
			{"--content", "Follow live content instead of compact events."},
		},
		Examples: []string{`wi agent monitor --follow`, `wi agent monitor --content --item auth`},
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
	printCommandRows(w, [][2]string{{"new", "Create a work item"}, {"list", "List work"}, {"show", "Show an item"}, {"search", "Find items by text"}, {"start", "Start backlog work"}, {"switch", "Open active work"}, {"next", "Open the next item needing attention"}, {"resume", "Ensure an active item's agent"}, {"wait", "Make an item wait"}, {"pick", "Choose an item and print its ID"}, {"blocker", "Edit an item's blockers"}, {"shelve", "Return active work to backlog"}, {"close", "Close work with a resolution"}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Metadata and history:")
	printCommandRows(w, [][2]string{{"state", "Inspect or set lifecycle state"}, {"edit", "Edit title, description, labels, and deep work"}, {"events", "Show item history"}, {"note", "Add a note to an item's log"}, {"notes", "Show an item's notes"}, {"attach", "Attach a file to an item"}, {"attachments", "List an item's attachments"}, {"attention", "Inspect, defer, or mark attention read"}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Agent and checkout:")
	printCommandRows(w, [][2]string{{"workspace", "Inspect or manage the worktree"}, {"terminal", "Inspect or manage tmux access"}, {"agent", "Inspect or control the primary agent"}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Administration:")
	printCommandRows(w, [][2]string{{"init", "Set up agent profiles and select the default"}, {"config", "Inspect global settings and agent profiles"}, {"merge", "Fast-forward a target branch, rebasing when needed"}, {"delete", "Delete closed work"}, {"shutdown", "Stop wi processes and terminals"}, {"daemon", "Inspect or control the daemon"}, {"plugin", "List and run optional plugins"}, {"info", "Show resolved paths"}, {"version", "Show version information"}})
	fmt.Fprintln(w)
	fmt.Fprintln(w, "States:")
	fmt.Fprintln(w, "  backlog | active | closed   (waiting active work stays out of the attention queue)")
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
