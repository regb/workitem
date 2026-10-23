package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/regb/workitem/internal/plugin"
	"sort"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	webaccess "github.com/regb/workitem/internal/web"
)

// webBackend adapts the CLI composition to the browser. Lifecycle, control and
// attention policy stay in the app and coordinator; this layer only shapes data.
type webBackend struct{ cfg Config }

func (b webBackend) resolve(id string) app.ResolveOptions {
	return app.ResolveOptions{Selector: id, CWD: b.cfg.CWD, Env: b.cfg.Env}
}

func (b webBackend) CheckItem(_ context.Context, id string) error {
	if _, err := b.cfg.App.Store.LoadManifest(id); errors.Is(err, store.ErrNotFound) {
		return webaccess.ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func (b webBackend) List(ctx context.Context, inspect bool) (webaccess.List, error) {
	if inspect {
		if _, err := b.cfg.Coordinator.ActivityBarrier(ctx); err != nil {
			return webaccess.List{}, err
		}
	}
	rules, err := effectiveListLabelRules(b.cfg.App.ListConfig.Labels, b.cfg.Env, nil)
	if err != nil {
		return webaccess.List{}, err
	}
	projection, err := b.cfg.Coordinator.ManifestProjection(ctx)
	if err != nil {
		return webaccess.List{}, err
	}
	result := viewapp.New(projectedManifestStore{manifests: projection.Manifests}).WorkList(app.WorkListOptions{LabelRules: rules}, b.cfg.App.DeepWorkConfig.MaxActive, b.cfg.App.ListConfig.RepositoryFolders)
	result.Warnings = append(result.Warnings, projection.Projection.Warnings...)
	if !projection.Projection.Fresh {
		result.Warnings = append(result.Warnings, "daemon manifest projection is stale")
	}
	agents, warnings := enrichWorkListAgentStatus(ctx, b.cfg, &result)
	result.Warnings = append(result.Warnings, warnings...)
	list, err := webList(result, agents, projection.Manifests)
	if err != nil {
		return list, err
	}
	list.Projects, err = b.Projects(ctx)
	return list, err
}

// webList reuses the CLI presentation buckets and ordering rather than
// inventing a web policy. Rows carry their repository ID for project filters.
func webList(result app.WorkListResult, agents map[string]string, manifests []model.Manifest) (webaccess.List, error) {
	// List rows omit full manifests. Read identity from the same projection
	// used to build the list, never from display names.
	repositories := map[string]model.Repository{}
	for _, manifest := range manifests {
		repositories[manifest.ID] = manifest.Repository
	}
	list := webaccess.List{Warnings: result.Warnings, DisplaySections: []webaccess.Section{}, Projects: []webaccess.Project{}}
	for _, section := range workDisplaySections(result, "active", agents) {
		rows := []webaccess.ListItem{}
		for _, item := range section.Items {
			repository, found := repositories[item.ID]
			if !found {
				return webaccess.List{}, fmt.Errorf("repository metadata unavailable for item %s", item.ID)
			}
			rows = append(rows, webListItem(item, model.RepositoryKey(repository)))
		}
		list.DisplaySections = append(list.DisplaySections, webaccess.Section{Name: section.Name, Items: rows})
	}
	return list, nil
}

// webListItem keeps what the dashboard uses from a work list row.
func webListItem(item app.WorkListItem, projectID string) webaccess.ListItem {
	row := webaccess.ListItem{ID: item.ID, Slug: item.Slug, Title: item.Title, State: item.State, Labels: item.Labels, Blockers: item.Blockers, Unread: item.Unread, ProjectID: projectID}
	if item.Agent != nil {
		row.Agent = &webaccess.ListAgent{Status: item.Agent.Status}
	}
	if item.Worktree != nil {
		row.Worktree = &webaccess.ListWorktree{Status: item.Worktree.Status, Reason: item.Worktree.Reason, Dirty: item.Worktree.Dirty, BranchMismatch: item.Worktree.BranchMismatch}
	}
	return row
}

// Projects come from the repository registry, including repositories without
// items; it owns project identity and names.
func (b webBackend) Projects(ctx context.Context) ([]webaccess.Project, error) {
	repositories, err := b.cfg.Coordinator.ListRepositories(ctx)
	if err != nil {
		return nil, err
	}
	return webProjects(repositories), nil
}

func webProjects(repositories []model.OwnedRepository) []webaccess.Project {
	projects := []webaccess.Project{}
	for _, repository := range repositories {
		projects = append(projects, webaccess.Project{ID: repository.ID, Name: repository.Name})
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Name != projects[j].Name {
			return projects[i].Name < projects[j].Name
		}
		return projects[i].ID < projects[j].ID
	})
	return projects
}

func (b webBackend) Agents(context.Context) ([]webaccess.AgentProfile, error) {
	defaultAgent := b.cfg.App.DefaultAgent
	profiles := make([]webaccess.AgentProfile, 0, len(b.cfg.App.AgentsConfig))
	for name, definition := range b.cfg.App.AgentsConfig {
		label := strings.TrimSpace(definition.Label)
		if label == "" {
			label = name
		}
		profiles = append(profiles, webaccess.AgentProfile{Name: name, Label: label})
	}
	sort.Slice(profiles, func(i, j int) bool {
		if (profiles[i].Name == defaultAgent) != (profiles[j].Name == defaultAgent) {
			return profiles[i].Name == defaultAgent
		}
		if profiles[i].Label != profiles[j].Label {
			return profiles[i].Label < profiles[j].Label
		}
		return profiles[i].Name < profiles[j].Name
	})
	return profiles, nil
}

// selectWebNext narrows the canonical queue to the selected projects; ranking
// and wrap policy stay shared with wi next.
func selectWebNext(queue coordinator.ActionabilityQueueResult, current string, projects []string) string {
	if projects != nil {
		allowed := map[string]bool{}
		for _, project := range projects {
			allowed[project] = true
		}
		filtered := []coordinator.ActionabilityQueueCandidate{}
		for _, candidate := range queue.Candidates {
			if allowed[model.RepositoryKey(candidate.Manifest.Repository)] {
				filtered = append(filtered, candidate)
			}
		}
		queue.Candidates = filtered
	}
	return coordinator.SelectActionability(queue, current).WorkItemID
}

func (b webBackend) Next(ctx context.Context, current string, projects []string) (string, error) {
	rules, err := effectiveListLabelRules(b.cfg.App.ListConfig.Labels, b.cfg.Env, nil)
	if err != nil {
		return "", err
	}
	result, err := b.cfg.Coordinator.ActionabilitySnapshot(ctx, coordinator.ActionabilitySnapshotRequest{CurrentItemID: current, Queue: coordinator.ActionabilityQueueOptions{Strategy: b.cfg.App.AttentionConfig.Priority, LabelRules: rules}})
	if err != nil {
		return "", err
	}
	return selectWebNext(result.Queue, current, projects), nil
}

func (b webBackend) Chat(ctx context.Context, id string) (webaccess.Chat, error) {
	shown, err := b.cfg.App.ShowWorkItem(ctx, b.resolve(id))
	if err != nil {
		return webaccess.Chat{}, err
	}
	m := shown.Manifest
	runtime, err := b.cfg.App.AgentRuntimeStatus(ctx, b.resolve(id))
	if err != nil {
		return webaccess.Chat{}, err
	}
	result := webaccess.Chat{Item: m, Description: shown.Description, Project: webaccess.Project{ID: model.RepositoryKey(m.Repository), Name: m.Repository.Name}, Runtime: runtime, Status: "unknown", History: driverprotocol.History{Messages: []driverprotocol.HistoryMessage{}}}
	if observation, err := b.cfg.Coordinator.AgentObservation(ctx, id); err == nil && observation.Found {
		result.Status = observation.Observation.Status
		observed := observation.Observation.Runtime
		result.Asking = result.Status == "idle" && observed != nil && observed.Readiness == "waiting_input" && driverprotocol.AwaitsAnswer(observed.ReadinessReasonCode)
		if result.Status == "problem" || result.Asking {
			result.StatusReason = observation.Observation.Reason
		}
		if !observation.Projection.Fresh {
			result.Warnings = append(result.Warnings, "Agent observation is stale.")
		}
	}
	// A live runtime reports its current branch; otherwise history follows
	// the last persisted leaf.
	leaf := ""
	if runtime.Online {
		if content, err := b.cfg.App.AgentContent(ctx, b.resolve(id)); err == nil {
			leaf = content.LeafID
		} else {
			result.Warnings = append(result.Warnings, err.Error())
		}
	}
	if history, err := b.cfg.App.ConversationHistory(ctx, b.resolve(id), leaf); err == nil {
		result.History = history
	} else {
		result.Warnings = append(result.Warnings, "History unavailable: "+err.Error())
	}
	if m.PrimaryConversation != nil && leaf == "" && runtime.Online && runtime.Runtime.Mode == "tui" {
		result.Warnings = append(result.Warnings, "History follows the last persisted branch; an older TUI's in-memory tree selection cannot be observed.")
	}
	switch m.State {
	case model.StateClosed:
		result.Warnings = append(result.Warnings, "Closed conversations are read-only.")
	case model.StateActive:
		if view, err := b.controlView(ctx, id); err == nil {
			result.Control = &view
		} else {
			result.Warnings = append(result.Warnings, "Control status unavailable: "+err.Error())
		}
		if activity, err := b.cfg.App.WorkItemActivity(ctx, b.resolve(id)); err == nil {
			result.Unread = activity.Activity.Unread()
		}
	}
	return result, nil
}

func (b webBackend) History(ctx context.Context, id, leaf, before string) (driverprotocol.History, error) {
	return b.cfg.App.ConversationHistoryPage(ctx, b.resolve(id), leaf, before)
}

// Files fuzzy-finds paths in the item's worktree for an @-mention.
func (b webBackend) Files(ctx context.Context, id, query string) ([]string, error) {
	m, err := b.cfg.App.ResolveItem(ctx, b.resolve(id))
	if err != nil {
		return nil, err
	}
	if !m.Checkout.Present() || m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" {
		return nil, errors.New("This item has no worktree yet.")
	}
	return findFiles(ctx, m.Checkout.Claim.Path, query)
}

// ProjectFiles fuzzy-finds paths on the project's integration branch.
func (b webBackend) ProjectFiles(ctx context.Context, project, query string) ([]string, error) {
	source, err := b.cfg.Coordinator.RepositorySource(ctx, project, "")
	if err != nil {
		return nil, err
	}
	return findCommitFiles(ctx, source.GitDir, source.Commit, query)
}

func (b webBackend) Content(ctx context.Context, id string) (driverprotocol.Content, error) {
	return b.cfg.App.AgentContent(ctx, b.resolve(id))
}

func (b webBackend) Changes(ctx context.Context, epoch string, after uint64, wait time.Duration) (webaccess.Changes, error) {
	result, err := b.cfg.Coordinator.Changes(ctx, coordinator.ChangesRequest{Epoch: epoch, After: after, WaitMillis: int(wait / time.Millisecond)})
	return webaccess.Changes{Epoch: result.Epoch, Revision: result.Revision, Items: result.ItemIDs, All: result.All}, err
}

func (b webBackend) ContentStream(ctx context.Context, id string, apply func(driverprotocol.ContentEvent) error) error {
	return b.cfg.App.AgentContentStream(ctx, b.resolve(id), apply)
}

const webNotIdle = "The agent is not idle. Queue this message explicitly, or wait for the current turn to finish."

// Send takes a vacant controller lease silently and delivers as that lease.
// Displacing another controller or interrupting a turn needs an explicit,
// confirmed takeover, which the page requests separately.
func (b webBackend) Send(ctx context.Context, id string, message webaccess.Message) (out webaccess.Outcome) {
	// The server admits sends only from a tab with an identity.
	client := webaccess.ClientID(ctx)
	ctx, asked := webApprovals(ctx, message.Approval)
	defer asked(&out)
	// A message to a backlog item starts it: the message is its first prompt.
	if m, err := b.cfg.App.ResolveItem(ctx, b.resolve(id)); err != nil {
		return webaccess.Outcome{Error: err.Error()}
	} else if m.State == model.StateBacklog {
		started, err := coordinatorStartWorkItem(ctx, b.cfg, id, false, false, agent.ModeHeadless, "work_item.started")
		if err != nil {
			return webaccess.Outcome{Error: err.Error(), Warnings: started.Workspace.Warnings}
		}
		// Like wi new --start, the first prompt waits for the new agent.
		if runtime := started.Workspace.AgentRuntime; runtime != nil {
			if err := b.cfg.App.WaitForAgentRuntimeReady(ctx, id, runtime); err != nil {
				return webaccess.Outcome{Error: err.Error(), Warnings: started.Workspace.Warnings}
			}
		}
	}
	control, refused := b.acquire(ctx, id, client, true, "send")
	if refused != nil {
		return *refused
	}
	if control.Runtime == nil || control.Lease == nil {
		return webaccess.Outcome{Error: "The agent runtime is not running; reload and try again.", Warnings: control.Warnings}
	}
	channels := control.Runtime.Capabilities.Channels()
	if channels.Control != model.ControlStructured {
		return webaccess.Outcome{Error: "This runtime only accepts terminal input; take control to restart it headless.", Warnings: control.Warnings}
	}
	// A replaced or newly started runtime supersedes the one the page saw.
	expected := message.RuntimeID
	if control.Plan.Replace != "" || control.Plan.Start != "" || expected == "" {
		expected = control.Runtime.ID
	}
	requireIdle := false
	if message.Delivery == "prompt" {
		if channels.AtomicIdle {
			requireIdle = true
		} else if b.cfg.App.AgentBusy(ctx, control.Runtime.WorkItemID) {
			// Without an atomic driver guard, refuse prompts against an observed
			// busy turn; the remaining race is reported by the capability.
			return webaccess.Outcome{Error: webNotIdle, Warnings: control.Warnings}
		}
	}
	result, err := b.cfg.App.AgentControl(ctx, webSendOptions(b.resolve(id), message, client, *control.Lease, expected, requireIdle))
	out = outcome(result, control.Warnings, err)
	if errors.Is(err, driverprotocol.ErrNotIdle) {
		out.Error = webNotIdle
	}
	return out
}

// webApprovals answers approvals needed while serving one web request, such
// as trusting a worktree .envrc, with the page's answer. Without an answer it
// records the question and refuses, so the page asks and resends; the
// server's terminal is never asked. The returned function reports the
// question on the outcome.
func webApprovals(ctx context.Context, answer string) (context.Context, func(*webaccess.Outcome)) {
	var asked *plugin.Approval
	ctx = plugin.WithApprover(ctx, func(_ context.Context, request plugin.Approval) (bool, error) {
		switch answer {
		case "allow":
			return true, nil
		case "deny":
			return false, nil
		}
		asked = &request
		return false, &plugin.ApprovalRequired{Approval: request}
	})
	return ctx, func(out *webaccess.Outcome) {
		if asked != nil {
			out.Approval = &webaccess.ApprovalRequest{Question: asked.Question, Path: asked.Path}
			out.Error = asked.Question
		}
	}
}

// webSendOptions submits as the tab's lease generation. Without it the daemon
// treats the send as a terminal command and rejects it while the tab controls.
func webSendOptions(resolve app.ResolveOptions, message webaccess.Message, client string, lease model.ControlLease, expectedRuntimeID string, requireIdle bool) app.AgentControlOptions {
	requestID := ""
	if message.ClientID != "" {
		requestID = "web:" + message.ClientID
	}
	return app.AgentControlOptions{ResolveOptions: resolve, CommandType: message.Delivery, Message: message.Text, Images: message.Images, Actor: "operator", RequestID: requestID, ExpectedRuntimeID: expectedRuntimeID, RequireIdle: requireIdle, ControllerHolder: model.LeaseHolderWeb, ControllerClientID: client, ControllerLeaseID: lease.ID}
}

func (b webBackend) controlRequest(id, client string, confirm bool) app.ControlRequest {
	return app.ControlRequest{ResolveOptions: b.resolve(id), Holder: model.LeaseHolderWeb, ClientID: client, Label: "web", Confirm: confirm, EnsureRuntime: true, TTL: app.WebLeaseTTL}
}

// controlView reports the lease as this tab sees it. Polling it renews the
// tab's own lease, so closing the tab lets control expire.
func (b webBackend) controlView(ctx context.Context, id string) (webaccess.ControlView, error) {
	client := webaccess.ClientID(ctx)
	status, plan, err := b.cfg.App.PlanControl(ctx, b.controlRequest(id, client, false))
	if err != nil {
		return webaccess.ControlView{}, err
	}
	view := webaccess.ControlView{Status: status, Plan: plan}
	if client != "" && status.Lease != nil && status.Lease.SameClient(model.LeaseHolderWeb, client) {
		view.You = true
		// Fence on the observed lease so a renewal never revives a replaced one.
		expected := status.Lease.ID
		if renewed, err := b.cfg.App.Leases.AcquireLease(ctx, model.LeaseRequest{ItemID: status.WorkItemID, Holder: model.LeaseHolderWeb, ClientID: client, Label: "web", ExpectedLeaseID: &expected, TTLSeconds: int(app.WebLeaseTTL / time.Second)}); err == nil && renewed.Granted {
			view.Status.Lease = renewed.Lease
		}
	}
	return view, nil
}

// controlViewOr falls back to the refused plan when the view cannot be read.
func (b webBackend) controlViewOr(ctx context.Context, id string, plan app.ControlPlan) *webaccess.ControlView {
	view, err := b.controlView(ctx, id)
	if err != nil {
		view = webaccess.ControlView{Plan: plan}
	}
	return &view
}

// acquire takes this tab's controller lease for an action (verb: "send",
// "stop it"). Another client's lease needs a confirmed takeover, so the
// refusal carries the control view the page offers it from.
func (b webBackend) acquire(ctx context.Context, id, client string, ensureRuntime bool, verb string) (app.ControlResult, *webaccess.Outcome) {
	request := b.controlRequest(id, client, false)
	request.EnsureRuntime = ensureRuntime
	control, err := b.cfg.App.AcquireControl(ctx, request)
	var confirmation *app.ControlConfirmationError
	if errors.As(err, &confirmation) {
		return control, &webaccess.Outcome{Error: "Another client controls this agent. Take control to " + verb + ".", Control: b.controlViewOr(ctx, id, confirmation.Plan)}
	}
	if err != nil {
		return control, &webaccess.Outcome{Error: err.Error(), Warnings: control.Warnings}
	}
	return control, nil
}

// outcome is an action's result as the page receives it.
func outcome(result any, warnings []string, err error) webaccess.Outcome {
	out := webaccess.Outcome{Result: result, Warnings: warnings}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

func (b webBackend) TakeControl(ctx context.Context, id string, request webaccess.TakeControl) webaccess.Outcome {
	result, err := b.cfg.App.AcquireControl(ctx, b.controlRequest(id, webaccess.ClientID(ctx), request.Confirm))
	out := outcome(result, result.Warnings, err)
	var confirmation *app.ControlConfirmationError
	if errors.As(err, &confirmation) {
		out.Control = b.controlViewOr(ctx, id, confirmation.Plan)
	}
	return out
}

// Stop aborts the agent's current turn, as this tab's controller. A runtime
// that offers no structured input (a terminal Claude) is stopped by taking
// control, which aborts its turn; nothing is started either way.
func (b webBackend) Stop(ctx context.Context, id string, request webaccess.TakeControl) webaccess.Outcome {
	client := webaccess.ClientID(ctx)
	runtime, err := b.cfg.App.AgentRuntimeStatus(ctx, b.resolve(id))
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	if !runtime.Online {
		return webaccess.Outcome{Error: "The agent is not running."}
	}
	acquired, refused := b.acquire(ctx, id, client, false, "stop it")
	if refused != nil {
		return *refused
	}
	if acquired.Plan.Replace != "" {
		// Taking control stopped the runtime, which aborted its turn.
		return webaccess.Outcome{Result: acquired, Warnings: acquired.Warnings}
	}
	if acquired.Lease == nil || acquired.Runtime == nil {
		return webaccess.Outcome{Error: "The agent is not running.", Warnings: acquired.Warnings}
	}
	result, err := b.cfg.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: b.resolve(id), CommandType: agent.CommandAbort, Actor: "operator", ExpectedRuntimeID: acquired.Runtime.ID, ControllerHolder: model.LeaseHolderWeb, ControllerClientID: client, ControllerLeaseID: acquired.Lease.ID})
	return outcome(result, acquired.Warnings, err)
}

func (b webBackend) ReleaseControl(ctx context.Context, id string) webaccess.Outcome {
	if err := b.cfg.App.ReleaseControl(ctx, b.resolve(id), model.LeaseHolderWeb, webaccess.ClientID(ctx)); err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	return webaccess.Outcome{Result: map[string]bool{"released": true}}
}

func (b webBackend) Block(ctx context.Context, id string, request webaccess.BlockRequest) webaccess.Outcome {
	blocker := model.Blocker{Kind: request.Kind, Event: strings.TrimSpace(request.Event), ItemID: request.ItemID}
	if request.Kind == model.BlockerTime {
		until, err := parseBlockUntil(time.Now(), strings.TrimSpace(request.Until))
		if err != nil {
			return webaccess.Outcome{Error: strings.ReplaceAll(err.Error(), "--until", "time")}
		}
		blocker.Until = &until
	}
	result, err := coordinatorBlockerCommand(ctx, b.cfg, id, []model.Blocker{blocker})
	return blockerOutcome(result, err)
}

func (b webBackend) ClearBlocker(ctx context.Context, id string, request webaccess.ClearBlockerRequest) webaccess.Outcome {
	result, err := coordinatorClearBlockers(ctx, b.cfg, id, strings.TrimSpace(request.BlockerID))
	return blockerOutcome(result, err)
}

func blockerOutcome(result app.BlockerResult, err error) webaccess.Outcome {
	if err != nil {
		return webaccess.Outcome{Result: result, Error: err.Error()}
	}
	return webaccess.Outcome{Result: result, Observed: &result.Manifest}
}

func (b webBackend) MarkRead(ctx context.Context, id string, request webaccess.ReadRequest) webaccess.Outcome {
	mark := b.cfg.App.MarkRead
	if request.Unread {
		mark = b.cfg.App.MarkUnread
	}
	result, err := mark(ctx, b.resolve(id))
	return outcome(result, nil, err)
}

func (b webBackend) Settings(ctx context.Context, id string) (app.AgentSettingsView, error) {
	return b.cfg.App.AgentSettings(ctx, b.resolve(id))
}

// SetSettings changes an item's agent settings. Changing a running agent is
// a control action: the tab takes a vacant lease silently and needs an
// explicit takeover otherwise, as for sending a message.
func (b webBackend) SetSettings(ctx context.Context, id string, request webaccess.SettingsRequest) webaccess.Outcome {
	options := app.SetAgentSettingsOptions{ResolveOptions: b.resolve(id), Settings: request.Settings, Restart: request.Restart}
	if status, err := b.cfg.App.AgentRuntimeStatus(ctx, b.resolve(id)); err == nil && status.Online {
		client := webaccess.ClientID(ctx)
		if client == "" {
			return webaccess.Outcome{Error: "This browser tab has no client identity; reload the page."}
		}
		control, refused := b.acquire(ctx, id, client, false, "change its settings")
		if refused != nil {
			return *refused
		}
		if control.Lease != nil {
			options.ControllerHolder, options.ControllerClientID, options.ControllerLeaseID = model.LeaseHolderWeb, client, control.Lease.ID
		}
	}
	result, err := b.cfg.App.SetAgentSettings(ctx, options)
	return outcome(result, result.Warnings, err)
}

// Start makes a backlog item active and sends its agent the configured
// startup message, exactly as if the page had sent it: like wi new --start,
// the agent reads the item's description itself.
func (b webBackend) Start(ctx context.Context, id string, request webaccess.StartRequest) webaccess.Outcome {
	out := b.Send(ctx, id, webaccess.Message{Text: b.cfg.App.AgentStartupMessage(""), Delivery: "prompt", ClientID: request.ClientID, Approval: request.Approval})
	return b.observed(out, id)
}

// Shelve returns active work to the backlog. The core stops an idle agent
// first and refuses a busy one.
func (b webBackend) Shelve(ctx context.Context, id string) webaccess.Outcome {
	result, err := b.cfg.App.Shelve(ctx, b.resolve(id), false)
	return b.observed(outcome(result, result.Warnings, err), id)
}

// Close runs the close workflow. Without force it refuses a busy agent; force
// aborts the turn first. Uncommitted changes always refuse, and steps that
// succeeded before a failure are retained.
func (b webBackend) Close(ctx context.Context, id string, request webaccess.CloseRequest) webaccess.Outcome {
	result, err := b.cfg.App.Close(ctx, b.resolve(id), request.Resolution, request.Force)
	out := outcome(result, result.Warnings, err)
	if err != nil && len(result.Warnings) > 0 {
		out.Warnings = append(out.Warnings, "Close did not complete. Retained successful steps are not rolled back.")
	}
	return b.observed(out, id)
}

// reread reads an item's durable state after a mutation. It runs on its own
// short deadline: the request that mutated may be out of time or cancelled.
func (b webBackend) reread(id string) (model.Manifest, error) {
	inspect, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return b.cfg.App.ResolveItem(inspect, b.resolve(id))
}

// observed re-reads durable state after a mutation, even a failed one, so the
// page never assumes that a failure changed nothing.
func (b webBackend) observed(out webaccess.Outcome, id string) webaccess.Outcome {
	m, err := b.reread(id)
	if err != nil {
		out.Warnings = append(out.Warnings, "Could not refresh durable state: "+strings.TrimSpace(err.Error()))
		return out
	}
	out.Observed = &m
	if out.Error != "" {
		out.Warnings = append(out.Warnings, "Observed lifecycle after attempt: "+m.State)
	}
	return out
}
