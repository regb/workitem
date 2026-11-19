# Review access

Review uses the ordinary operator API and socket, like other Wi commands. Runtime startup does not issue a review token or install a review-only endpoint in the agent environment. There are no review credential headers, credential projections, runtime-principal checks, or review-specific CLI socket routing.

The explicit `WI_AGENT_DAEMON_SOCKET` restriction is unchanged. When set, it applies to every command, including review and plugins, and must be an absolute socket path. Selecting a review command or changing caller headers cannot bypass the endpoint's authority.

The restricted agent endpoint retains its existing safe backlog/item operations. It does not allow review RPCs, workspace access, lifecycle, agent control, terminal operations, deletion or shutdown. Review commands from an explicitly sandboxed process are denied rather than silently routed to the operator socket.

The private runtime-host endpoint is also unchanged apart from removing the review credential method. Hosts can report their fenced runtime ownership and compact semantic events; they cannot acquire operator authority or run item/workspace/lifecycle mutations through that endpoint.

Workspace claim/generation checks, checkout authority checks, native event ownership fencing, controller leases, and uncertain-message delivery semantics remain independent of review credentials. No new sandbox or cloud credential scheme is introduced here, and protected agent instructions are unchanged.
