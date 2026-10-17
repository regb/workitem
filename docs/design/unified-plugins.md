# Unified plugins

Status: implemented for native preparation and executable jobs/commands. This
records the common plugin model used by direnv, GitHub, and explicitly registered
third-party manifests. Executable preparation hooks remain deferred.

## Objective and trust model

One plugin system for adding features outside Wi's core. First-party and
third-party plugins use the same registration and configuration model.

Plugins are fully trusted operator code. Enabling one authorizes it to execute
code and use Wi as the operator. There is no per-plugin permission system,
capability approval workflow, action-proposal broker, or claim of sandboxing.

First-party versus third-party describes provenance. Native versus executable
describes execution. Neither creates a separate plugin platform.

Optimize for a single local operator, simple implementation, and useful extension
points. This is not a package manager or a replacement for agent drivers.

## What stays in core

Core retains item lifecycle, blocker rules, checkout generations, runtime
ownership, and the daemon's single-writer model. Plugins can use these features
through the normal CLI or API. They do not write directly to `wi.db`.

This is a correctness and maintainability boundary, not a restriction on trusted
code. A GitHub plugin can add or clear blockers using ordinary Wi commands. We
do not need a separate proposal protocol to mediate those operations.

Existing revision checks, locks, validation, and idempotent commands remain
important regardless of whether a human or a plugin calls them.

## One registration model

A plugin definition describes:

- Its stable name and implementation.
- Its option schema and defaults.
- The hooks, jobs, commands, and instruction assets it contributes.

These contributions describe what Wi should invoke. They are not permissions
that the plugin must request or that the operator must approve individually.

Bundled definitions come from the build. External definitions are explicitly
registered through operator configuration and static metadata. Listing and
configuration inspection should not execute plugin code. Duplicate identities,
settings namespaces, and command names are errors.

Use an ordinary enabled/disabled setting for optional plugins. Do not add
per-contribution grants or approval records. No project-local file may silently
install or enable a plugin.

The MVP does not need package downloads, signatures, descriptor pinning, a
permission language, or automatic discovery by executing programs on PATH.

## One settings model

Reuse the existing option registry and resolution order:

```text
registered defaults -> global user defaults -> repository overrides
```

- Global defaults remain manually authored user configuration.
- Repository overrides remain daemon-owned settings keyed by repository ID.
- Schemas declare types, defaults, choices, sensitivity, and repository scope.
- The host resolves settings and passes a copied snapshot to the implementation.
- Plugins do not implement their own inheritance or configuration-file parsing.
- Unknown plugin settings remain stored but inactive; invalid known values fail
  validation rather than silently changing behavior.
- Inspection hides sensitive and unknown values unless explicitly requested.

Keep `extensions.direnv` as its existing canonical namespace. For the first
implementation, use `extensions.NAME` for the other plugins too. A historical
word in a configuration key is less costly than two settings systems or an
unnecessary migration. There must not be two writable aliases for the same policy.

Replace the pending opaque `[plugins.NAME.config]` behavior settings with this
registered model. Implementation paths and activation remain installation
configuration, distinct from the plugin's behavior options.

Read repository settings again for subsequent operations and job runs. Global
registration and scheduler changes take effect on host reload or restart. Do not
add automatic daemon restarts or reconfigure existing agents in this work.

## Contributions and execution

| Contribution | Purpose | Contract |
| --- | --- | --- |
| Preparation hook | Prepare a checkout or command/runtime environment | Existing typed requests, patches, warnings, and errors |
| Job | Periodic work such as checking GitHub | Context, effective settings, ordinary CLI/API access, and completion status |
| Command | An explicitly invoked plugin feature | Arguments, selected context, effective settings, output, and exit status |
| Instructions | Teach agents how to use an enabled feature | Bounded static assets included in new runtimes |

One plugin may provide any combination of these. They belong to one registry
without pretending they are all the same kind of callback.

Use native calls for code compiled into Wi and an executable adapter for external
implementations. Both use the same identity, option schema, and contribution
registration. First-party executables get no separate implementation convention.

Start with the execution machinery needed by the two real integrations:

- Direnv keeps its native preparation hooks.
- GitHub uses executable jobs and commands, calling Wi and `gh` normally.
- Instruction assets are read without launching a process at agent startup.

Jobs and commands do not need a new bidirectional host RPC protocol. Keep their
process interface small. A versioned external preparation protocol can be added
when there is an actual consumer that needs typed patches or interactive approval.
One system does not require every adapter to support every contribution on day one.

Retain timeouts, process-group cancellation, bounded output, private logs, and
shutdown joins. Those prevent mistakes from hanging startup or filling memory;
they are not a security boundary against trusted executable code.

## Direnv

Direnv becomes a first-party native plugin in the common registry. Its existing
`load` and `trust` behavior and repository settings remain unchanged.

Keep sanitation, command environment, runtime environment, and checkout
preparation as explicit ordered hooks. Validate patches and protect host identity
variables. Do not pass the whole App or database into preparation code when a
small typed request is sufficient. This keeps dependencies understandable without
inventing a permission system.

Disabling evaluation must still remove inherited direnv state and revoke stale
reused-slot approval. Keep that cleanup implementation registered even when
`load = false` or the direnv executable is absent. Do not introduce a generic
unload feature that accidentally removes required environment hygiene.

Approval of project shell code remains separate from trust in the plugin itself.
Enabling the direnv integration must not silently approve every `.envrc`.

## GitHub

GitHub becomes a first-party executable plugin in the same registry. It provides:

- Registered behavior settings such as ignored authors and polling interval.
- A command such as `wi github wait PR`.
- A scheduled job that checks GitHub-tagged external blockers.
- Static agent instructions for those commands.

The command verifies the PR and adds its blocker through the normal Wi CLI/API.
The job lists relevant items, checks their PRs, and clears satisfied blockers
through that same interface. Keep the PR interpretation and polling policy in the
plugin. Core should know nothing about GitHub.

Correctness still matters: missing identity or a failed inspection must not clear
a blocker, and the checker should act only on the external blockers explicitly
tagged for it. These are GitHub feature rules, not host-enforced plugin grants.

Enabling the checker can cover the installation by default. Repository settings
may narrow its behavior where useful; do not require repository-by-repository
security approval. A command should clearly report if automatic checking is off.

## Host responsibilities

- Invoke registered hooks in deterministic order and preserve partial outcomes.
- Schedule enabled jobs without overlap for the same job/scope, with bounded
  concurrency and cancellation. Missed polls do not require a replay queue.
- Route namespaced commands without shadowing built-ins or registering arbitrary
  global flags. Prefer keeping the short `wi github ...` form.
- Read and validate instruction assets and include them only in new runtimes.
- Keep plugin execution outside bbolt transactions and the structured-write loop.
  Preparation retains its existing item-operation locking where needed.
- Supply resolved settings and useful context rather than making each plugin
  reconstruct host configuration.

Scheduled jobs run as the operator. Existing restricted agent endpoints remain a
separate Wi concern, not a sandbox for trusted plugins. Preserve explicit endpoint
routing rather than accidentally changing it while cleaning item context. This
plan does not redesign agent endpoint policy or add a plugin permission model.

Plugins may launch other commands and access operator resources. Installing a
third-party plugin is therefore the same kind of trust decision as running an
operator-installed tool, not granting a limited Wi-specific permission token.

## Implementation map

1. `internal/plugin` contains the catalog, registrations, preparation pipeline,
   settings snapshots, static assets, executable runner and scheduler.
2. `internal/plugins/bundled` supplies direnv and GitHub definitions and loads
   declarative third-party metadata before validating configuration.
3. `internal/plugins/direnv` implements native hooks. Its schema, trust settings
   and cleanup behavior are unchanged; the old parallel extension packages are gone.
4. CLI composition uses this host for preparation, commands, jobs, instructions
   and daemon settings validation. `config extensions` remains the schema query.
5. GitHub uses registered `ignore_authors` and `interval_seconds` options, refreshes
   policy per repository/job run and retains its normal CLI-based implementation.

Completion means both integrations use one catalog and settings resolver, while
new jobs or commands can be added without service-specific core changes. Test
collisions, malformed metadata, removed plugins, rename/name reuse, false/empty
values, redaction, stale revisions, partial outcomes, process cancellation, output
bounds, and TUI/headless preparation using isolated roots.

No operator configuration or trust is migrated implicitly. The former uncommitted
opaque external settings and interval fields are rejected with guidance. This
integration does not activate plugins, restart a daemon, or edit native conversations.

## Scope decisions

1. Keep the historical `extensions.NAME` settings keys during unification?
   Retained; a rename would be a separate explicit change.
2. Keep short namespaced commands such as `wi github ...`?
   Retained, with collision checks in the common registry.
3. Add executable preparation hooks now?
   Deferred. The implemented adapters cover native direnv and executable GitHub;
   add external hook transport when another plugin needs it.

## Related work

- [Current direnv extraction and repository settings](../direnv-extension-design.md).
- [Lifecycle, blockers, and controller design](lifecycle-blockers-control.md).
