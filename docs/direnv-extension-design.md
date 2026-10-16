# Bundled extensions and repository settings

Direnv is the first bundled, in-process extension. Repository overrides are daemon-owned entity settings, not tables or generated files in user configuration. There is no dynamic loader, external extension protocol, or approval sharing.

## Package boundaries

- `internal/options` provides declarative registration, type/scope validation, resolution, and redaction. It does not execute extensions.
- `internal/extension` provides synchronous preparation ports and ordered dispatch. Hooks have no application service or coordinator client.
- `internal/extensions/direnv` owns direnv commands, formats, policy, inherited-state cleanup, approval requests, and slot-reuse revocation.
- `internal/extensions/bundled` is host composition. It resolves settings through a supplied repository reader before invoking hooks.
- `internal/direnv` remains the system adapter.
- Workspace and primary-agent core contain no direnv-specific ports, parser, commands, or configuration.

Core retains repository identity, lifecycle, checkout generations, runtime ownership, terminal access, and structured-write authority. Extension context does not authorize lifecycle or workspace control.

## Global defaults versus entity policy

The manually maintained user config contains extension defaults:

```toml
[extensions.direnv]
load = true
trust = "prompt"
```

Repository-specific policy belongs to the registered repository:

```sh
wi repo settings show biskoui
wi repo settings set biskoui extensions.direnv.trust always
wi repo settings set biskoui extensions.direnv.load false
wi repo settings unset biskoui extensions.direnv.trust
```

Names and IDs select the repository. Internally settings are keyed by immutable repository ID in a separate canonical record in `wi.db`; they are not copied into Git configuration or onboarding checkouts. Rename and archive preserve settings. Name reuse cannot transfer authorization.

Each setting uses a namespace such as `extensions.direnv` and a registered key such as `trust`. The option definition declares type, default, repository-scope permission, sensitivity, description, and optional closed choices.

The daemon validates set operations against trusted schemas supplied at composition. A client cannot register a schema in a mutation request. Global-only, unknown, incorrectly typed, and invalid enum values are rejected. Unset also permits cleanup of an existing unavailable setting.

Values resolve as registered default, global user default, then repository override. Explicit false, zero, and empty values are distinct from omission. Arrays replace rather than append. Unsetting restores inheritance.

Checked-in `.config/wi.toml` remains a safe label-default subset. It cannot register code, configure extensions, or grant trust. User-config repository tables and the old direnv path allowlist are rejected with actionable instructions, not silently migrated.

## Canonical storage and writes

A repository settings record contains its repository ID, a monotonic revision, and bounded namespaced JSON values. The daemon is the only writer. Mutations use an expected revision and transactionally update the record, append a compact change event, and persist an idempotent acknowledgement.

Values are limited to 8 KiB per option and 64 KiB per repository. Acknowledgements contain only result metadata and an input digest. Events contain repository identity, namespace, key, operation, and revision, never setting values. Secret values remain in private canonical state, not event history.

Reads atomically resolve repository identity and settings. Missing optional settings mean revision zero with no overrides; malformed stored data is an error, not permission to fall back to broader trust. Failed history writes roll back settings and acknowledgement updates with the same bbolt transaction.

Settings read/write endpoints are operator-only. An unavailable extension does not erase its stored namespace. Operators can inspect it with redaction or remove it. Unknown namespaces are inactive; invalid values for installed extensions prevent their application.

## Host resolution and inspection

The host supplies a repository reader to bundled extension composition. Settings are read lazily and frozen per repository for the CLI operation. Hooks receive copied, validated, resolved option values. They never access the database or merge configuration themselves.

Repository lookup failure, identity mismatch, or invalid known settings fails preparation. It must not silently substitute global policy, which might grant broader trust. A later operation gets a fresh snapshot. Existing runtime environments remain unchanged.

`wi repo settings show --json` exposes stored overrides, effective values, origins, revision, and warnings. Namespace keys use `extensions.NAME`. Sensitive and unavailable settings are redacted unless explicitly revealed. `wi config` remains a local, global-default inspection command and never contacts the daemon.

## Preparation ports

The pipeline implements four synchronous phases:

1. Checkout preparation after materialization and before claim publication, with repository/item identity, reserved path, next generation, and reuse state.
2. Inherited-environment sanitization.
3. Command environment preparation, limited to host-approved project setting keys.
4. Runtime environment preparation before TUI or headless process creation.

Checkout preparation is a narrow workspace port, not a general event bus inside core. Application composition connects it to the pipeline. Operation locking and generation checks remain in place. Cancellation or failure prevents claim publication and later hooks. Earlier filesystem effects, such as revoking stale approval, are not silently undone.

Hooks run in explicit registration order and receive copied environment maps and options. They return patches, warnings, or errors. The host validates keys and protects control/identity variables. Sanitizers remove values only; command hooks can change only requested keys. Cancellation is checked before and after hooks. Effects must tolerate retries; there is no exactly-once promise.

No extension callback runs inside a bbolt transaction or the coordinator's structured-write loop. Schema validation is declarative, not execution of plugin callbacks.

## Environment isolation

TUI composition selects its tmux-server base and removes stale session values. Headless composition does not inspect tmux. Both use the same preparation ports and install the result as a replacement environment, not an overlay that can reintroduce caller secrets.

The direnv extension decodes inherited `DIRENV_DIFF` state. Disabling loading does not disable cleanup or stale-approval revocation. The bundled sanitizer stays registered even when the executable is absent. Removing the integration entirely would require an explicit clean-inheritance strategy; dynamic unloading is not implemented.

## Direnv behavior

- `load = false` prevents evaluation, not inherited-state cleanup.
- `trust = "prompt"` requests interactive approval before granting new trust. Without approval, startup reports the omitted environment.
- `trust = "always"` permits automatic approval, including changed contents.
- `trust = "never"` never grants new approval; previously approved environments can still be used.
- Command-setting reads never grant approval.

Approval and revocation apply only to regular checkout-local `.envrc` files. Canonical ancestor aliases are supported, but symlink files and ancestors outside the verified boundary cannot be approved or revoked. Command reads may use an already approved enclosing file within caller/checkout context. A changed file invalidates a pending approval.

Slot reuse still revokes approval before publishing the new claim. Identical `.envrc` bytes may execute different branch-local files, so content-only approval sharing is not the default. Any future reusable approval records must be separate from policy and use narrowly scoped daemon-owned writes.

## Deferred work

- External-process discovery, manifests, and versioned transport.
- Dynamic CLI flag registration.
- Additional registered core entity settings and safe checked-in extension options.
- Dependency-aware approval sharing and revocation records.

These should build on the option registry and preparation ports rather than restoring direnv-specific core fields.
