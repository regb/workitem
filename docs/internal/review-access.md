# Review access

Review uses the ordinary API and socket, like other Wi commands, with the caller's credential: the operator's, or an agent's item credential. There is no review-specific token, endpoint or socket routing; review is authorized like every other call.

The per-feature review credential this note once described was removed in favor of general item credentials: every agent runtime gets one, and the daemon resolves every call's principal from it. See the API reference's endpoint and credentials section.

The private runtime-host endpoint is also unchanged apart from removing the review credential method. Hosts can report their fenced runtime ownership and compact semantic events; they cannot acquire operator authority or run item/workspace/lifecycle mutations through that endpoint.

Workspace claim/generation checks, checkout authority checks, native event ownership fencing, controller leases, and uncertain-message delivery semantics remain independent of review credentials. No new sandbox or cloud credential scheme is introduced here, and protected agent instructions are unchanged.
