package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	driver "github.com/regb/workitem/agentdriver/protocol"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/itemnotes"
	webaccess "github.com/regb/workitem/internal/web"
)

// webBackend only translates browser requests and presentation data. Every
// domain operation runs in the daemon through generated clients.
type webBackend struct{ cfg Config }

func webRequest[T any](ctx context.Context, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	client := webaccess.ClientID(ctx)
	if client == "" {
		client = "web"
	}
	req.Header().Set("Wi-Client-Id", client)
	req.Header().Set("Wi-Client-Kind", "web")
	return req
}
func (b webBackend) CheckItem(ctx context.Context, id string) error {
	_, err := b.cfg.API.Items.GetItem(ctx, webRequest(ctx, &v1.GetItemRequest{ItemId: id}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return webaccess.ErrNotFound
	}
	return err
}
func webRow(v *v1.ItemSummary) webaccess.ListItem {
	row := webaccess.ListItem{ID: v.Id, Slug: v.Slug, Title: v.Title, State: apiview.State(v.State), Resolution: apiview.Resolution(v.Resolution), Labels: apiview.Strings(v.Labels), Blockers: apiview.Blockers(v.Blockers), ProjectID: v.RepositoryId}
	if o := v.Observation; o != nil {
		row.Unread = o.Unread
		if o.AgentStatus != 0 {
			row.Agent = &webaccess.ListAgent{Status: apiview.Status(o.AgentStatus)}
		}
		if o.WorktreeStatus != 0 {
			row.Worktree = &webaccess.ListWorktree{Status: apiview.Worktree(o.WorktreeStatus), Reason: o.WorktreeReason, Dirty: o.WorktreeDirty, BranchMismatch: o.BranchMismatch}
		}
	}
	return row
}

// webSectionOrder is the dashboard's sections, in the order and with the
// names the page folds and colors by, as wi list shows them.
var webSectionOrder = []string{"NEEDS ATTENTION", "NEEDS FIXING", "IN PROGRESS", "WAITING", "BACKLOG", "CLOSED"}

// webSection places a row as wi list does: the daemon's attention group,
// with active work whose agent or checkout has a problem apart under NEEDS
// FIXING.
func webSection(row *v1.ItemSummary) string {
	switch row.AttentionGroup {
	case v1.AttentionGroup_ATTENTION_GROUP_WAITING:
		return "WAITING"
	case v1.AttentionGroup_ATTENTION_GROUP_BACKLOG:
		return "BACKLOG"
	case v1.AttentionGroup_ATTENTION_GROUP_CLOSED:
		return "CLOSED"
	}
	if o := row.Observation; o != nil && (o.AgentStatus == v1.AgentStatus_AGENT_STATUS_PROBLEM || o.WorktreeStatus == v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM) {
		return "NEEDS FIXING"
	}
	if row.AttentionGroup == v1.AttentionGroup_ATTENTION_GROUP_WORKING {
		return "IN PROGRESS"
	}
	return "NEEDS ATTENTION"
}

func (b webBackend) List(ctx context.Context, inspect bool) (webaccess.List, error) {
	configRules := []string{}
	if b.cfg.GlobalConfig != nil {
		configRules = b.cfg.GlobalConfig.List.Labels
	}
	rules, err := effectiveListLabelRules(configRules, b.cfg.Env, nil)
	if err != nil {
		return webaccess.List{}, err
	}
	value, err := b.cfg.API.Items.ListItems(ctx, webRequest(ctx, &v1.ListItemsRequest{States: []v1.ItemState{v1.ItemState_ITEM_STATE_ACTIVE, v1.ItemState_ITEM_STATE_BACKLOG, v1.ItemState_ITEM_STATE_CLOSED}, Order: v1.ListOrder_LIST_ORDER_ATTENTION, Inspect: inspect, LabelRules: apiLabelRules(rules), ClosedLimit: 50}))
	if err != nil {
		return webaccess.List{}, err
	}
	out := webaccess.List{Warnings: apiview.Strings(value.Msg.Warnings), DisplaySections: []webaccess.Section{}, Projects: []webaccess.Project{}}
	sections := map[string][]webaccess.ListItem{}
	for _, row := range value.Msg.Items {
		name := webSection(row)
		sections[name] = append(sections[name], webRow(row))
	}
	for _, name := range webSectionOrder {
		if items := sections[name]; len(items) > 0 {
			out.DisplaySections = append(out.DisplaySections, webaccess.Section{Name: name, Items: items})
		}
	}
	out.Projects, err = b.Projects(ctx)
	return out, err
}
func (b webBackend) Projects(ctx context.Context) ([]webaccess.Project, error) {
	value, err := b.cfg.API.Repositories.ListRepositories(ctx, webRequest(ctx, &v1.ListRepositoriesRequest{}))
	if err != nil {
		return nil, err
	}
	out := []webaccess.Project{}
	for _, repo := range value.Msg.Repositories {
		out = append(out, webaccess.Project{ID: repo.Id, Name: repo.Name})
	}
	return out, nil
}
func (b webBackend) Agents(ctx context.Context) ([]webaccess.AgentProfile, error) {
	value, err := b.cfg.API.Profiles.ListAgentProfiles(ctx, webRequest(ctx, &v1.ListAgentProfilesRequest{}))
	if err != nil {
		return nil, err
	}
	out := []webaccess.AgentProfile{}
	for _, p := range value.Msg.Profiles {
		out = append(out, webaccess.AgentProfile{Name: p.Name, Label: p.Label})
	}
	return out, nil
}
func (b webBackend) Search(ctx context.Context, query, state string) (webaccess.SearchResult, error) {
	req := &v1.SearchItemsRequest{Query: query}
	if state != "" {
		v := v1.ItemState(v1.ItemState_value["ITEM_STATE_"+strings.ToUpper(state)])
		if v == 0 {
			return webaccess.SearchResult{}, fmt.Errorf("unknown state %q", state)
		}
		req.States = []v1.ItemState{v}
	}
	value, err := b.cfg.API.Items.SearchItems(ctx, webRequest(ctx, req))
	if err != nil {
		return webaccess.SearchResult{}, err
	}
	out := webaccess.SearchResult{Items: []webaccess.SearchHit{}, Total: int(value.Msg.Total)}
	for _, h := range value.Msg.Hits {
		out.Items = append(out.Items, webaccess.SearchHit{ListItem: webRow(h.Item), Field: h.Field, Excerpt: h.Excerpt})
	}
	return out, nil
}
func (b webBackend) Next(ctx context.Context, current string, projects []string) (string, error) {
	value, err := b.cfg.API.Items.ListItems(ctx, webRequest(ctx, &v1.ListItemsRequest{Order: v1.ListOrder_LIST_ORDER_ATTENTION, RepositoryIds: projects, States: []v1.ItemState{v1.ItemState_ITEM_STATE_ACTIVE}}))
	if err != nil {
		return "", err
	}
	if projects != nil && len(projects) == 0 {
		return "", nil
	}
	for _, row := range value.Msg.Items {
		if row.Observation != nil && row.Observation.NeedsAttention && row.Id != current {
			return row.Id, nil
		}
	}
	return "", nil
}
func (b webBackend) Chat(ctx context.Context, id string) (webaccess.Chat, error) {
	value, err := b.cfg.API.Views.GetItemView(ctx, webRequest(ctx, &v1.GetItemViewRequest{ItemId: id}))
	if err != nil {
		return webaccess.Chat{}, err
	}
	v := value.Msg
	m := apiview.Manifest(v.Item)
	out := webaccess.Chat{Item: m, Description: v.Description, Project: webaccess.Project{ID: m.Repository.ID, Name: m.Repository.Name}, Runtime: apiview.Runtime(id, v.Agent), History: apiview.History(v.History), Warnings: apiview.Strings(v.Warnings), ReadAt: apiview.Time(v.ReadAt).UnixMicro()}
	if v.Agent != nil {
		out.Status = apiview.Status(v.Agent.Status)
		out.StatusReason = v.Agent.Reason
		out.Asking = v.Agent.Asking
	}
	if v.Observation != nil {
		out.Unread = v.Observation.Unread
	}
	if v.Control != nil {
		status, plan := apiview.Control(id, v.Control, v.Agent)
		out.Control = &webaccess.ControlView{Status: status, Plan: plan, You: v.Control.Yours}
	}
	if v.Setup != nil && v.Setup.State != v1.SetupState_SETUP_STATE_SUCCEEDED {
		r := &app.SetupRun{ClaimID: v.Setup.ClaimId, StartedAt: apiview.Time(v.Setup.StartedAt), Error: v.Setup.Error, Warnings: v.Setup.Warnings}
		if v.Setup.State == v1.SetupState_SETUP_STATE_RUNNING {
			r.Status = app.SetupRunning
		} else {
			r.Status = app.SetupFailed
		}
		out.Setup = &app.SetupStatus{Current: true, Run: r}
	}
	return out, nil
}
func (b webBackend) History(ctx context.Context, id, leaf, before string) (driver.History, error) {
	value, err := b.cfg.API.Agents.GetHistory(ctx, webRequest(ctx, &v1.GetHistoryRequest{ItemId: id, LeafId: leaf, Before: before}))
	if err != nil {
		return driver.History{}, err
	}
	return apiview.History(value.Msg), nil
}
func (b webBackend) SubagentHistory(ctx context.Context, id, sub, before string) (driver.History, error) {
	value, err := b.cfg.API.Agents.GetSubagentHistory(ctx, webRequest(ctx, &v1.GetSubagentHistoryRequest{ItemId: id, SubagentId: sub, Before: before}))
	if err != nil {
		return driver.History{}, err
	}
	return apiview.History(&v1.GetHistoryResponse{Messages: value.Msg.Messages, NextBefore: value.Msg.NextBefore, Truncated: value.Msg.Truncated}), nil
}
func (b webBackend) Files(ctx context.Context, id, query string) ([]string, error) {
	value, err := b.cfg.API.Workspaces.SearchWorkspaceFiles(ctx, webRequest(ctx, &v1.SearchWorkspaceFilesRequest{ItemId: id, Query: query}))
	if err != nil {
		return nil, err
	}
	return value.Msg.Paths, nil
}
func (b webBackend) ProjectFiles(ctx context.Context, id, query string) ([]string, error) {
	value, err := b.cfg.API.Repositories.SearchRepositoryFiles(ctx, webRequest(ctx, &v1.SearchRepositoryFilesRequest{RepositoryId: id, Query: query}))
	if err != nil {
		return nil, err
	}
	return value.Msg.Paths, nil
}
func (b webBackend) Content(ctx context.Context, id string) (driver.Content, error) {
	stream, err := b.cfg.API.Agents.WatchLiveContent(ctx, webRequest(ctx, &v1.WatchLiveContentRequest{ItemId: id}))
	if err != nil {
		return driver.Content{}, err
	}
	defer stream.Close()
	if !stream.Receive() {
		return driver.Content{}, stream.Err()
	}
	content := apiview.Content(stream.Msg().GetSnapshot())
	content.WorkItemID = id
	return content, nil
}
func (b webBackend) ContentStream(ctx context.Context, id string, apply func(driver.ContentEvent) error) error {
	stream, err := b.cfg.API.Agents.WatchLiveContent(ctx, webRequest(ctx, &v1.WatchLiveContentRequest{ItemId: id}))
	if err != nil {
		return err
	}
	defer stream.Close()
	sequence := uint64(0)
	for stream.Receive() {
		sequence++
		if err := apply(apiview.ContentEvent(stream.Msg(), sequence)); err != nil {
			return err
		}
	}
	return stream.Err()
}
func (b webBackend) Changes(ctx context.Context, epoch string, after uint64, wait time.Duration) (webaccess.Changes, error) {
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	stream, err := b.cfg.API.Events.WatchChanges(waitCtx, webRequest(waitCtx, &v1.WatchChangesRequest{After: epoch}))
	if err != nil {
		return webaccess.Changes{}, err
	}
	defer stream.Close()
	for stream.Receive() {
		v := stream.Msg()
		if epoch == "" || v.Cursor != epoch || v.All || len(v.ItemIds) > 0 {
			return webaccess.Changes{Epoch: v.Cursor, Revision: 1, Items: v.ItemIds, All: v.All}, nil
		}
	}
	if ctx.Err() == nil && connect.CodeOf(stream.Err()) == connect.CodeDeadlineExceeded {
		return webaccess.Changes{Epoch: epoch, Revision: after}, nil
	}
	return webaccess.Changes{}, stream.Err()
}
func outcome(result any, warnings []string, err error) webaccess.Outcome {
	out := webaccess.Outcome{Result: result, Warnings: warnings}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}
func (b webBackend) observed(ctx context.Context, out webaccess.Outcome, id string) webaccess.Outcome {
	value, err := b.cfg.API.Items.GetItem(ctx, webRequest(ctx, &v1.GetItemRequest{ItemId: id}))
	if err == nil {
		m := apiview.Manifest(value.Msg.Item)
		out.Observed = &m
	}
	return out
}
func (b webBackend) job(ctx context.Context, id string, job *v1.Job, answer string) webaccess.Outcome {
	if job == nil {
		return b.observed(ctx, webaccess.Outcome{}, id)
	}
	stream, err := b.cfg.API.Jobs.WatchJob(ctx, webRequest(ctx, &v1.WatchJobRequest{JobId: job.Id}))
	if err != nil {
		return b.observed(ctx, outcome(nil, nil, err), id)
	}
	defer stream.Close()
	for stream.Receive() {
		value := stream.Msg().Job
		switch value.State {
		case v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL:
			if answer != "" {
				decision := v1.ApprovalDecision_APPROVAL_DECISION_DENY
				if answer == "allow" {
					decision = v1.ApprovalDecision_APPROVAL_DECISION_ALLOW
				}
				_, err := b.cfg.API.Jobs.AnswerApproval(ctx, webRequest(ctx, &v1.AnswerApprovalRequest{JobId: value.Id, ChallengeId: value.Approval.ChallengeId, Decision: decision}))
				if err != nil {
					return b.observed(ctx, outcome(nil, nil, err), id)
				}
				answer = ""
				continue
			}
			return b.observed(ctx, webaccess.Outcome{Result: value, Approval: &webaccess.ApprovalRequest{Question: value.Approval.Question, Path: value.Approval.Path}, Error: value.Approval.Question}, id)
		case v1.JobState_JOB_STATE_SUCCEEDED:
			return b.observed(ctx, outcome(value, value.Warnings, nil), id)
		case v1.JobState_JOB_STATE_FAILED, v1.JobState_JOB_STATE_CANCELLED:
			return b.observed(ctx, outcome(value, value.Warnings, errors.New(value.Error)), id)
		}
	}
	return b.observed(ctx, outcome(nil, nil, stream.Err()), id)
}
func (b webBackend) pendingApproval(ctx context.Context, id, answer string) (webaccess.Outcome, bool) {
	jobs, err := b.cfg.API.Jobs.ListJobs(ctx, webRequest(ctx, &v1.ListJobsRequest{ItemId: id, RunningOnly: true}))
	if err != nil {
		return outcome(nil, nil, err), true
	}
	for _, job := range jobs.Msg.Jobs {
		if job.State == v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL {
			return b.job(ctx, id, job, answer), true
		}
	}
	return webaccess.Outcome{}, false
}
func (b webBackend) Send(ctx context.Context, id string, message webaccess.Message) webaccess.Outcome {
	if out, pending := b.pendingApproval(ctx, id, message.Approval); pending {
		return out
	}
	item, err := b.cfg.API.Items.GetItem(ctx, webRequest(ctx, &v1.GetItemRequest{ItemId: id}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	if item.Msg.Item.State == v1.ItemState_ITEM_STATE_BACKLOG || item.Msg.Item.Checkout == nil || item.Msg.Item.Checkout.Claim == nil {
		value, err := b.cfg.API.Lifecycle.StartItem(ctx, webRequest(ctx, &v1.StartItemRequest{ItemId: id, Start: &v1.StartOptions{Prompt: message.Text, Mode: v1.AgentMode_AGENT_MODE_HEADLESS, ClientMessageId: message.ClientID}}))
		if err != nil {
			return b.observed(ctx, outcome(nil, nil, err), id)
		}
		return b.job(ctx, id, value.Msg.Job, message.Approval)
	}
	delivery := v1.Delivery(v1.Delivery_value["DELIVERY_"+strings.ToUpper(strings.ReplaceAll(message.Delivery, "-", "_"))])
	generation, _ := strconv.ParseUint(message.RuntimeID, 10, 32)
	r := &v1.SendMessageRequest{ItemId: id, Text: message.Text, Delivery: delivery, ClientMessageId: message.ClientID, ExpectedGeneration: uint32(generation), StartMode: v1.AgentMode_AGENT_MODE_HEADLESS}
	for _, image := range message.Images {
		data, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			return outcome(nil, nil, err)
		}
		r.Images = append(r.Images, &v1.Image{MimeType: image.MIMEType, Data: data})
	}
	value, err := b.cfg.API.Agents.SendMessage(ctx, webRequest(ctx, r))
	if err != nil {
		return b.observed(ctx, outcome(nil, nil, err), id)
	}
	if value.Msg.Job != nil {
		return b.job(ctx, id, value.Msg.Job, message.Approval)
	}
	return b.observed(ctx, outcome(value.Msg, nil, nil), id)
}
func (b webBackend) TakeControl(ctx context.Context, id string, request webaccess.TakeControl) webaccess.Outcome {
	value, err := b.cfg.API.Control.AcquireControl(ctx, webRequest(ctx, &v1.AcquireControlRequest{ItemId: id, ClientId: webaccess.ClientID(ctx), Label: webaccess.User(ctx), Confirm: request.Confirm, PlanToken: request.PlanToken, StartAgent: true}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	if value.Msg.Job != nil {
		return b.job(ctx, id, value.Msg.Job, "")
	}
	return outcome(value.Msg, nil, nil)
}
func (b webBackend) Stop(ctx context.Context, id string, request webaccess.TakeControl) webaccess.Outcome {
	control, err := b.cfg.API.Control.AcquireControl(ctx, webRequest(ctx, &v1.AcquireControlRequest{ItemId: id, ClientId: webaccess.ClientID(ctx), Confirm: request.Confirm, PlanToken: request.PlanToken}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	if control.Msg.Control.Lease == nil {
		return outcome(nil, nil, errors.New("no control lease"))
	}
	value, err := b.cfg.API.Agents.AbortTurn(ctx, webRequest(ctx, &v1.AbortTurnRequest{ItemId: id, LeaseId: control.Msg.Control.Lease.Id}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, nil, nil)
}
func (b webBackend) ReleaseControl(ctx context.Context, id string) webaccess.Outcome {
	control, err := b.cfg.API.Control.GetControl(ctx, webRequest(ctx, &v1.GetControlRequest{ItemId: id, ClientId: webaccess.ClientID(ctx)}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	if control.Msg.Control.Lease == nil {
		return webaccess.Outcome{}
	}
	value, err := b.cfg.API.Control.ReleaseControl(ctx, webRequest(ctx, &v1.ReleaseControlRequest{LeaseId: control.Msg.Control.Lease.Id}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, nil, nil)
}
func (b webBackend) Block(ctx context.Context, id string, request webaccess.BlockRequest) webaccess.Outcome {
	r := &v1.AddBlockerRequest{ItemId: id, Kind: v1.BlockerKind(v1.BlockerKind_value["BLOCKER_KIND_"+strings.ToUpper(request.Kind)]), BlockingItemId: request.ItemID, Event: request.Event}
	if request.Until != "" {
		t, err := time.Parse(time.RFC3339Nano, request.Until)
		if err != nil {
			return outcome(nil, nil, err)
		}
		r.Until = timestamp(t)
	}
	value, err := b.cfg.API.Items.AddBlocker(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return b.observed(ctx, outcome(value.Msg, nil, nil), id)
}
func (b webBackend) ClearBlocker(ctx context.Context, id string, request webaccess.ClearBlockerRequest) webaccess.Outcome {
	value, err := b.cfg.API.Items.ClearBlocker(ctx, webRequest(ctx, &v1.ClearBlockerRequest{ItemId: id, BlockerId: request.BlockerID, ClearAll: request.BlockerID == ""}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return b.observed(ctx, outcome(value.Msg, nil, nil), id)
}
func (b webBackend) MarkRead(ctx context.Context, id string, request webaccess.ReadRequest) webaccess.Outcome {
	value, err := b.cfg.API.Items.MarkRead(ctx, webRequest(ctx, &v1.MarkReadRequest{ItemId: id, Unread: request.Unread}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, nil, nil)
}
func (b webBackend) Notes(ctx context.Context, id string) (webaccess.Notes, error) {
	value, err := b.cfg.API.Items.ListNotes(ctx, webRequest(ctx, &v1.ListNotesRequest{ItemId: id}))
	if err != nil {
		return webaccess.Notes{}, err
	}
	out := webaccess.Notes{Notes: []itemnotes.Note{}, Attachments: []itemnotes.Current{}}
	for _, n := range value.Msg.Notes {
		out.Notes = append(out.Notes, apiview.Note(n))
	}
	for _, a := range value.Msg.Attachments {
		out.Attachments = append(out.Attachments, itemnotes.Current{Attachment: apiview.Attachment(a.Attachment), Time: apiview.Time(a.Time)})
	}
	return out, nil
}
func (b webBackend) Attachment(ctx context.Context, id, digest string) (io.ReadCloser, itemnotes.Attachment, error) {
	stream, err := b.cfg.API.Items.ReadAttachment(ctx, webRequest(ctx, &v1.ReadAttachmentRequest{ItemId: id, Sha256: digest}))
	if err != nil {
		return nil, itemnotes.Attachment{}, err
	}
	defer stream.Close()
	var data bytes.Buffer
	meta := itemnotes.Attachment{}
	for stream.Receive() {
		v := stream.Msg()
		if v.Attachment != nil {
			meta = apiview.Attachment(v.Attachment)
		}
		data.Write(v.Chunk)
	}
	if stream.Err() != nil {
		return nil, meta, stream.Err()
	}
	return io.NopCloser(bytes.NewReader(data.Bytes())), meta, nil
}
func (b webBackend) AddNote(ctx context.Context, id string, request webaccess.NoteRequest) webaccess.Outcome {
	value, err := b.cfg.API.Items.AddNote(ctx, webRequest(ctx, &v1.AddNoteRequest{ItemId: id, Text: request.Text}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(apiview.Note(value.Msg.Note), nil, nil)
}
func (b webBackend) Attach(ctx context.Context, id string, request webaccess.AttachRequest) webaccess.Outcome {
	value, err := b.cfg.API.Items.Attach(ctx, webRequest(ctx, &v1.AttachRequest{ItemId: id, Name: request.Name, Content: request.Content}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(apiview.Note(value.Msg.Note), nil, nil)
}
