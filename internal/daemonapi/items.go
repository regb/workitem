package daemonapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/app"
	view "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
)

func (s *Server) CreateItem(ctx context.Context, req *connect.Request[v1.CreateItemRequest]) (*connect.Response[v1.CreateItemResponse], error) {
	unlock := s.lock(":create")
	defer unlock()
	r := req.Msg
	commandID := id()
	if r.RequestId != "" {
		sum := sha256.Sum256([]byte(string(apitransport.Caller(ctx)) + ":" + r.RequestId))
		commandID = "api-create:" + hex.EncodeToString(sum[:])
		if saved, found, err := s.Core.Database().CreatedItem(commandID); err != nil {
			return nil, err
		} else if found {
			value, err := s.item(ctx, saved.Manifest.ID)
			return response(&v1.CreateItemResponse{Item: value}, err)
		}
	}
	if !model.ValidID(r.RepositoryId) {
		return nil, invalid("valid repository_id is required")
	}
	if r.GenerateTitle && apitransport.Caller(ctx) == apitransport.Agent {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("agent backlog capture cannot request model title generation"))
	}
	if r.AgentProfile != "" {
		if _, found := s.App.Agents()[r.AgentProfile]; !found {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent profile %q not found", r.AgentProfile))
		}
	}
	if r.Title == "" && !r.GenerateTitle {
		return nil, invalid("title is required unless generate_title is set")
	}
	source, err := s.Local.RepositorySource(ctx, r.RepositoryId, r.Base)
	if err != nil {
		return nil, rpcError(err)
	}
	if source.Repository.Archived {
		return nil, precondition("repository is archived")
	}
	var syncWarnings []string
	if r.Base == "" && s.Config.Repo.FollowUpstream != "off" {
		view, err := s.Local.RepositoryViewSource(ctx, r.RepositoryId, "")
		if err != nil {
			return nil, rpcError(err)
		}
		if view.Repository.Upstream != nil {
			if _, err := s.Local.SyncRepository(ctx, coordinator.RepositorySyncRequest{Selector: r.RepositoryId, Mode: "ff"}); err != nil {
				syncWarnings = append(syncWarnings, "default base synchronization failed; using local integration branch: "+err.Error())
			}
			source, err = s.Local.RepositorySource(ctx, r.RepositoryId, "")
			if err != nil {
				return nil, rpcError(err)
			}
		}
	}
	repo := &model.Repository{ID: source.Repository.ID, Name: source.Repository.Name, IntegrationBranch: source.Repository.IntegrationBranch, RootAtCreation: source.GitDir, GitCommonDir: source.GitDir, CreatedFromCommit: source.Commit}
	prepared, err := s.App.PrepareNewWorkItem(ctx, app.NewWorkItemOptions{Repository: repo, Title: r.Title, Untitled: r.GenerateTitle && r.Title == "", Slug: r.Slug, Description: r.Description, Labels: r.Labels, NoDefaultLabels: true, DeepWork: r.DeepWork, Agent: r.AgentProfile, AgentSettings: r.AgentSettings})
	if err != nil {
		return nil, rpcError(err)
	}
	created, err := s.Local.CreateItem(ctx, coordinator.CreateItemRequest{Command: coordinator.CreateItemCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Manifest: prepared.Manifest, DescriptionDigest: coordinator.DescriptionDigest(r.Description), Actor: "user", CreatedAt: prepared.Manifest.CreatedAt}, Description: r.Description})
	if err != nil {
		if retained, found, _ := s.Core.Database().CreatedItem(commandID); found {
			return nil, partial(err, retained.Manifest.ID, "created item")
		}
		return nil, rpcError(err)
	}
	if r.GenerateTitle {
		s.startJob("suggest_title", created.Manifest.ID, r.RepositoryId, func(ctx context.Context, _ *jobRun) error {
			title, err := s.App.GenerateItemTitle(ctx, resolve(created.Manifest.ID), r.Description)
			if err != nil {
				return err
			}
			unlock := s.lock(created.Manifest.ID)
			defer unlock()
			current, err := s.canonical(ctx, created.Manifest.ID, 0)
			if err != nil {
				return err
			}
			description, err := coordinator.ReadEditDescription(s.App.Store, created.Manifest.ID)
			if err != nil {
				return err
			}
			if current.Manifest.Title != prepared.Manifest.Title || description != r.Description {
				return nil
			}
			_, err = s.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: created.Manifest.ID, ExpectedRevision: uint32(current.Revision), Title: &title}))
			return err
		})
	}
	value, err := s.item(ctx, created.Manifest.ID)
	return response(&v1.CreateItemResponse{Item: value, Warnings: append(prepared.Warnings, syncWarnings...)}, err)
}
func (s *Server) ResolveItem(ctx context.Context, req *connect.Request[v1.ResolveItemRequest]) (*connect.Response[v1.ResolveItemResponse], error) {
	if req.Msg.Path != "" && !filepath.IsAbs(req.Msg.Path) {
		return nil, invalid("path must be absolute")
	}
	m, err := s.App.ResolveItem(ctx, app.ResolveOptions{Selector: req.Msg.Selector, CWD: req.Msg.Path})
	return response(&v1.ResolveItemResponse{ItemId: m.ID}, err)
}
func (s *Server) observation(ctx context.Context, m model.Manifest) *v1.ItemObservation {
	out := &v1.ItemObservation{WorktreeStatus: v1.WorktreeStatus_WORKTREE_STATUS_ABSENT}
	if observed, err := s.Local.AgentObservation(ctx, m.ID); err == nil && observed.Found {
		v := observed.Observation
		out.AgentStatus = agentStatus(v.Status)
		out.AgentReason = v.Reason
		out.ObservedAt = stamp(v.ObservedAt)
		if v.Runtime != nil {
			out.Asking = v.Runtime.Readiness == "waiting_input"
		}
		if w := v.Worktree; w != nil {
			out.WorktreeStatus = worktreeStatus(w.Status)
			out.WorktreeReason = w.Reason
			out.WorktreeDirty = w.Dirty
			out.BranchMismatch = w.BranchMismatch
		}
	}
	if activity, err := s.Local.AttentionActivity(ctx, m.ID); err == nil {
		a := activity.Activity
		out.Activity = &v1.AttentionActivity{LastRequestedAt: optionalStamp(a.LastRequestedAt), LastCompletedAt: optionalStamp(a.LastCompletedAt), LastReadAt: optionalStamp(a.LastReadAt), LastUnreadAt: optionalStamp(a.LastUnreadAt), LastDeferredAt: optionalStamp(a.LastDeferredAt)}
		out.Unread = (view.Activity{LastRequestedAt: a.LastRequestedAt, LastCompletedAt: a.LastCompletedAt, LastReadAt: a.LastReadAt, LastUnreadAt: a.LastUnreadAt, LastDeferredAt: a.LastDeferredAt}).Unread()
	}
	out.NeedsAttention = m.State == model.StateActive && (out.Unread || (!m.Waiting(time.Now()) && (out.AgentStatus == v1.AgentStatus_AGENT_STATUS_IDLE || out.AgentStatus == v1.AgentStatus_AGENT_STATUS_PROBLEM || out.WorktreeStatus == v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM || out.BranchMismatch || out.Asking)))
	if m.SnoozedUntil != nil && m.SnoozedUntil.After(time.Now()) && !out.Unread {
		out.NeedsAttention = false
	}
	if m.State == model.StateClosed {
		out.AgentStatus = 0
		out.NeedsAttention = false
	}
	return out
}
func (s *Server) GetItem(ctx context.Context, req *connect.Request[v1.GetItemRequest]) (*connect.Response[v1.GetItemResponse], error) {
	c, err := s.canonical(ctx, req.Msg.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.GetItemResponse{Item: item(c.Manifest, c.Revision), Observation: s.observation(ctx, c.Manifest)}
	if req.Msg.IncludeDescription {
		out.Description, err = coordinator.ReadEditDescription(s.App.Store, c.Manifest.ID)
	}
	if apitransport.Caller(ctx) == apitransport.Agent {
		out.Item.Checkout.Claim = nil
		out.Item.Conversation = nil
	}
	return response(out, err)
}
func (s *Server) summary(ctx context.Context, m model.Manifest) *v1.ItemSummary {
	return &v1.ItemSummary{Id: m.ID, Slug: m.Slug, Title: m.Title, State: state(m.State), Resolution: resolution(m.Resolution), Labels: m.Labels, DeepWork: m.DeepWork, RepositoryId: m.Repository.ID, Blockers: blockers(m.Blockers), Observation: s.observation(ctx, m), Priority: m.Priority, Pinned: m.Pinned, SnoozedUntil: optionalStamp(m.SnoozedUntil), UpdatedAt: stamp(m.UpdatedAt)}
}
func allowedState(states []v1.ItemState, m model.Manifest, defaults bool) bool {
	if len(states) == 0 {
		return !defaults || m.State != model.StateClosed
	}
	for _, state := range states {
		if stateName(state) == m.State {
			return true
		}
	}
	return false
}
func labelMatch(rules []*v1.LabelRule, labels []string) bool {
	for _, rule := range rules {
		switch v := rule.Rule.(type) {
		case *v1.LabelRule_Require:
			if !model.HasLabel(labels, v.Require) {
				return false
			}
		case *v1.LabelRule_Exclude:
			if model.HasLabel(labels, v.Exclude) {
				return false
			}
		case *v1.LabelRule_Unlabeled:
			if v.Unlabeled && len(labels) > 0 {
				return false
			}
		}
	}
	return true
}
func (s *Server) ListItems(ctx context.Context, req *connect.Request[v1.ListItemsRequest]) (*connect.Response[v1.ListItemsResponse], error) {
	r := req.Msg
	for _, v := range r.States {
		if v < 1 || v > 3 {
			return nil, invalid("unknown item state")
		}
	}
	if r.Inspect {
		if _, err := s.Local.ActivityBarrier(ctx); err != nil {
			return nil, rpcError(err)
		}
	}
	manifests, err := s.Local.ListManifests(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	ranks := map[string]int{}
	if r.Order == v1.ListOrder_LIST_ORDER_ATTENTION {
		q, err := s.Local.ActionabilitySnapshot(ctx, coordinator.ActionabilitySnapshotRequest{Queue: coordinator.ActionabilityQueueOptions{Strategy: s.Config.Attention.Priority}})
		if err != nil {
			return nil, rpcError(err)
		}
		for i, c := range q.Queue.Candidates {
			ranks[c.Manifest.ID] = i + 1
		}
	}
	out := &v1.ListItemsResponse{Items: []*v1.ItemSummary{}}
	for _, m := range manifests {
		if !allowedState(r.States, m, true) || !labelMatch(r.LabelRules, m.Labels) {
			continue
		}
		if len(r.RepositoryIds) > 0 {
			found := false
			for _, repo := range r.RepositoryIds {
				if repo == m.Repository.ID {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		row := s.summary(ctx, m)
		if r.Order == v1.ListOrder_LIST_ORDER_ATTENTION {
			switch {
			case m.State == model.StateClosed:
				row.AttentionGroup = v1.AttentionGroup_ATTENTION_GROUP_CLOSED
			case m.State == model.StateBacklog:
				row.AttentionGroup = v1.AttentionGroup_ATTENTION_GROUP_BACKLOG
			case row.Observation.NeedsAttention:
				row.AttentionGroup = v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION
			case m.Waiting(time.Now()) || m.SnoozedUntil != nil && m.SnoozedUntil.After(time.Now()):
				row.AttentionGroup = v1.AttentionGroup_ATTENTION_GROUP_WAITING
			default:
				row.AttentionGroup = v1.AttentionGroup_ATTENTION_GROUP_WORKING
			}
		}
		out.Items = append(out.Items, row)
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if r.Order == v1.ListOrder_LIST_ORDER_ATTENTION {
			if a.AttentionGroup != b.AttentionGroup {
				return a.AttentionGroup < b.AttentionGroup
			}
			if a.Pinned != b.Pinned {
				return a.Pinned
			}
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			ra, rb := ranks[a.Id], ranks[b.Id]
			if ra != 0 && rb != 0 && ra != rb {
				return ra < rb
			}
		}
		if !a.UpdatedAt.AsTime().Equal(b.UpdatedAt.AsTime()) {
			return a.UpdatedAt.AsTime().After(b.UpdatedAt.AsTime())
		}
		return a.Id < b.Id
	})
	closedLimit := r.ClosedLimit
	if closedLimit == 0 {
		closedLimit = 50
	}
	closed := uint32(0)
	filtered := out.Items[:0]
	for _, row := range out.Items {
		if row.State == v1.ItemState_ITEM_STATE_CLOSED {
			closed++
			if closed > closedLimit {
				continue
			}
		}
		filtered = append(filtered, row)
	}
	out.Items = filtered
	offset := 0
	if r.PageToken != "" {
		data, err := base64.RawURLEncoding.DecodeString(r.PageToken)
		if err != nil {
			return nil, invalid("invalid page_token")
		}
		offset, err = strconv.Atoi(string(data))
		if err != nil || offset < 0 || offset > len(out.Items) {
			return nil, invalid("invalid page_token")
		}
	}
	out.Items = out.Items[offset:]
	if r.PageSize > 0 && len(out.Items) > int(r.PageSize) {
		out.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + int(r.PageSize))))
		out.Items = out.Items[:r.PageSize]
	}
	return response(out, nil)
}
func (s *Server) SearchItems(ctx context.Context, req *connect.Request[v1.SearchItemsRequest]) (*connect.Response[v1.SearchItemsResponse], error) {
	found := view.New(s.App.Store).Search(view.SearchOptions{Query: req.Msg.Query, Limit: 1 << 30}, s.Config.List.RepositoryFolders, func(m model.Manifest) view.SearchText {
		description, _ := coordinator.ReadEditDescription(s.App.Store, m.ID)
		notes, _ := itemnotes.Read(s.App.Store.ItemDir(m.ID))
		extra := view.SearchText{Description: description}
		for _, n := range notes {
			extra.Notes = append(extra.Notes, n.Text)
			if n.Attachment != nil {
				extra.Notes = append(extra.Notes, n.Attachment.Name)
			}
		}
		return extra
	})
	out := &v1.SearchItemsResponse{Hits: []*v1.SearchHit{}}
	limit := req.Msg.Limit
	if limit == 0 {
		limit = 50
	}
	for _, hit := range found.Items {
		m, err := s.App.Store.LoadManifest(hit.ID)
		if err != nil {
			return nil, rpcError(err)
		}
		if !allowedState(req.Msg.States, m, false) {
			continue
		}
		out.Total++
		if uint32(len(out.Hits)) < limit {
			out.Hits = append(out.Hits, &v1.SearchHit{Item: s.summary(ctx, m), Field: hit.Field, Excerpt: hit.Excerpt})
		}
	}
	return response(out, nil)
}
func (s *Server) command(ctx context.Context, itemID, kind string, expected uint32, patch func(*coordinator.ManifestCommand)) (coordinator.ManifestCommandResult, error) {
	c, err := s.canonical(ctx, itemID, expected)
	if err != nil {
		return coordinator.ManifestCommandResult{}, err
	}
	command := coordinator.ManifestCommand{ID: id(), ProtocolVersion: coordinator.ProtocolVersion, Type: kind, ItemID: itemID, ExpectedRevision: &c.Revision, Actor: "user", MaxActive: s.Config.DeepWork.MaxActive, CreatedAt: time.Now().UTC()}
	patch(&command)
	return s.Local.ExecuteManifestCommand(ctx, command)
}
func (s *Server) UpdateItem(ctx context.Context, req *connect.Request[v1.UpdateItemRequest]) (*connect.Response[v1.UpdateItemResponse], error) {
	r := req.Msg
	c, err := s.canonical(ctx, r.ItemId, r.ExpectedRevision)
	if err != nil {
		return nil, rpcError(err)
	}
	if r.SnoozedUntil != nil && r.ClearSnooze {
		return nil, invalid("snoozed_until and clear_snooze conflict")
	}
	edit := &coordinator.MetadataEdit{Title: r.Title, AddLabels: r.AddLabels, RemoveLabels: r.RemoveLabels, DeepWork: r.DeepWork, Priority: r.Priority, Pinned: r.Pinned, ClearSnooze: r.ClearSnooze}
	if r.SnoozedUntil != nil {
		if err := r.SnoozedUntil.CheckValid(); err != nil {
			return nil, invalid(err.Error())
		}
		t := r.SnoozedUntil.AsTime()
		edit.SnoozedUntil = &t
	}
	if r.Description != nil {
		old, err := coordinator.ReadEditDescription(s.App.Store, r.ItemId)
		if err != nil {
			return nil, rpcError(err)
		}
		edit.PreviousDescriptionDigest = coordinator.DescriptionDigest(old)
		edit.DescriptionDigest = coordinator.DescriptionDigest(*r.Description)
	}
	result, err := s.Local.EditItem(ctx, coordinator.EditItemRequest{Command: coordinator.ManifestCommand{ID: id(), ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandEdit, ItemID: r.ItemId, ExpectedRevision: &c.Revision, Actor: "user", Edit: edit, CreatedAt: time.Now().UTC()}, Description: r.Description})
	if err != nil {
		if after, readErr := s.canonical(ctx, r.ItemId, 0); readErr == nil && !reflect.DeepEqual(after.Manifest, c.Manifest) {
			return nil, partial(err, r.ItemId, "updated item")
		}
	}
	return response(&v1.UpdateItemResponse{Item: item(result.Manifest, result.Revision)}, err)
}
func (s *Server) SetItemState(ctx context.Context, req *connect.Request[v1.SetItemStateRequest]) (*connect.Response[v1.SetItemStateResponse], error) {
	r := req.Msg
	c, err := s.canonical(ctx, r.ItemId, r.ExpectedRevision)
	if err != nil {
		return nil, rpcError(err)
	}
	if r.State < 1 || r.State > 3 {
		return nil, invalid("valid state is required")
	}
	if c.Manifest.State == stateName(r.State) {
		return response(&v1.SetItemStateResponse{Item: item(c.Manifest, c.Revision)}, nil)
	}
	if c.Manifest.State == model.StateClosed && r.State == v1.ItemState_ITEM_STATE_ACTIVE {
		return nil, precondition("reopen to backlog before activating")
	}
	if r.State != v1.ItemState_ITEM_STATE_ACTIVE {
		if c.Manifest.Checkout.Claim != nil {
			return nil, precondition("item still holds a workspace claim")
		}
		running, err := s.App.AgentRuntimeStatus(ctx, resolve(r.ItemId))
		if err != nil {
			return nil, err
		}
		if running.Online {
			return nil, precondition("agent is still running")
		}
	}
	if r.State == v1.ItemState_ITEM_STATE_CLOSED && (r.Resolution < 1 || r.Resolution > 3) {
		return nil, invalid("closing requires a resolution")
	}
	result, err := s.command(ctx, r.ItemId, coordinator.CommandStateSet, r.ExpectedRevision, func(c *coordinator.ManifestCommand) {
		c.Force = r.OverrideCapacity && (apitransport.Caller(ctx) == apitransport.Operator || apitransport.Caller(ctx) == apitransport.WebAdapter)
		c.TargetState = stateName(r.State)
		c.Resolution = resolutionName(r.Resolution)
	})
	if err != nil {
		if after, readErr := s.canonical(ctx, r.ItemId, 0); readErr == nil && after.Manifest.State == stateName(r.State) {
			return nil, partial(err, r.ItemId, "changed item state")
		}
	}
	return response(&v1.SetItemStateResponse{Item: item(result.Manifest, result.Revision)}, err)
}
func (s *Server) DeleteItem(ctx context.Context, req *connect.Request[v1.DeleteItemRequest]) (*connect.Response[v1.DeleteItemResponse], error) {
	if !req.Msg.Confirm {
		return nil, precondition("deletion requires confirm")
	}
	c, err := s.canonical(ctx, req.Msg.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	if c.Manifest.State != model.StateClosed || c.Manifest.Checkout.Claim != nil {
		return nil, precondition("delete requires a closed, released item")
	}
	running, err := s.App.AgentRuntimeStatus(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, err
	}
	if running.Online {
		return nil, precondition("agent is still running")
	}
	err = s.App.Store.RemoveItem(req.Msg.ItemId)
	if err != nil {
		if _, readErr := s.canonical(ctx, req.Msg.ItemId, 0); readErr != nil {
			return nil, partial(err, req.Msg.ItemId, "deleted item record")
		}
	}
	return response(&v1.DeleteItemResponse{}, err)
}
func (s *Server) MarkRead(ctx context.Context, req *connect.Request[v1.MarkReadRequest]) (*connect.Response[v1.MarkReadResponse], error) {
	c, err := s.canonical(ctx, req.Msg.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	observation := s.observation(ctx, c.Manifest)
	if req.Msg.Unread {
		if observation.Unread {
			return response(&v1.MarkReadResponse{}, nil)
		}
		_, err := s.App.MarkUnread(ctx, resolve(req.Msg.ItemId))
		return response(&v1.MarkReadResponse{}, err)
	}
	if req.Msg.HistoryLeafId != "" {
		history, err := s.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: req.Msg.ItemId, LeafId: req.Msg.HistoryLeafId}))
		if err != nil {
			return nil, err
		}
		var at time.Time
		for _, message := range history.Msg.Messages {
			if message.Time != nil && message.Time.AsTime().After(at) {
				at = message.Time.AsTime()
			}
		}
		if at.IsZero() {
			return response(&v1.MarkReadResponse{}, nil)
		}
		if observation.Activity != nil && observation.Activity.LastReadAt != nil && !at.After(observation.Activity.LastReadAt.AsTime()) {
			return response(&v1.MarkReadResponse{}, nil)
		}
		err = s.App.Store.AppendEvent(ctx, req.Msg.ItemId, model.NewEvent(at, "attention.read", "user", map[string]any{"at": at, "history_leaf_id": req.Msg.HistoryLeafId}))
		return response(&v1.MarkReadResponse{}, err)
	}
	if !observation.Unread && observation.Activity != nil && observation.Activity.LastReadAt != nil {
		return response(&v1.MarkReadResponse{}, nil)
	}
	_, err = s.App.MarkRead(ctx, resolve(req.Msg.ItemId))
	return response(&v1.MarkReadResponse{}, err)
}
func (s *Server) DeferItem(ctx context.Context, req *connect.Request[v1.DeferItemRequest]) (*connect.Response[v1.DeferItemResponse], error) {
	if _, err := s.canonical(ctx, req.Msg.ItemId, 0); err != nil {
		return nil, rpcError(err)
	}
	value, err := s.Local.AttentionActivity(ctx, req.Msg.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	a := value.Activity
	if a.LastDeferredAt != nil {
		newActivity := false
		for _, at := range []*time.Time{a.LastRequestedAt, a.LastCompletedAt, a.LastUnreadAt} {
			if at != nil && at.After(*a.LastDeferredAt) {
				newActivity = true
			}
		}
		if !newActivity {
			return response(&v1.DeferItemResponse{}, nil)
		}
	}
	_, err = s.App.RecordAttentionDefer(ctx, resolve(req.Msg.ItemId))
	return response(&v1.DeferItemResponse{}, err)
}
func (s *Server) ListItemEvents(ctx context.Context, req *connect.Request[v1.ListItemEventsRequest]) (*connect.Response[v1.ListItemEventsResponse], error) {
	if _, err := s.canonical(ctx, req.Msg.ItemId, 0); err != nil {
		return nil, rpcError(err)
	}
	events, err := s.Core.Database().ListItemEvents(req.Msg.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.ListItemEventsResponse{}
	offset := 0
	if req.Msg.PageToken != "" {
		data, e := base64.RawURLEncoding.DecodeString(req.Msg.PageToken)
		if e != nil {
			return nil, invalid("invalid page_token")
		}
		offset, e = strconv.Atoi(string(data))
		if e != nil || offset < 0 || offset > len(events) {
			return nil, invalid("invalid page_token")
		}
	}
	end := len(events)
	if req.Msg.PageSize > 0 && end > offset+int(req.Msg.PageSize) {
		end = offset + int(req.Msg.PageSize)
		out.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	for _, e := range events[offset:end] {
		out.Events = append(out.Events, &v1.ItemEvent{Id: e.ID, Type: e.Type, Actor: e.Actor, Time: stamp(e.Timestamp), PayloadJson: string(e.Payload)})
	}
	return response(out, nil)
}
func (s *Server) AddBlocker(ctx context.Context, req *connect.Request[v1.AddBlockerRequest]) (*connect.Response[v1.AddBlockerResponse], error) {
	r := req.Msg
	if r.Kind < 1 || r.Kind > 3 {
		return nil, invalid("valid blocker kind is required")
	}
	if r.Until != nil {
		if err := r.Until.CheckValid(); err != nil {
			return nil, invalid(err.Error())
		}
	}
	b := model.Blocker{Kind: strings.ToLower(strings.TrimPrefix(r.Kind.String(), "BLOCKER_KIND_")), ItemID: r.BlockingItemId, Event: r.Event, Label: r.Label, Meta: r.Meta}
	if r.Until != nil {
		t := r.Until.AsTime()
		b.Until = &t
	}
	result, err := s.command(ctx, r.ItemId, coordinator.CommandBlockerAdd, 0, func(c *coordinator.ManifestCommand) { c.Blockers = []model.Blocker{b} })
	out := &v1.AddBlockerResponse{Item: item(result.Manifest, result.Revision)}
	if len(result.Manifest.Blockers) > 0 {
		out.Blocker = blockers(result.Manifest.Blockers)[len(result.Manifest.Blockers)-1]
	}
	return response(out, err)
}
func (s *Server) ClearBlocker(ctx context.Context, req *connect.Request[v1.ClearBlockerRequest]) (*connect.Response[v1.ClearBlockerResponse], error) {
	if req.Msg.BlockerId == "" && !req.Msg.ClearAll {
		return nil, invalid("blocker_id or clear_all is required")
	}
	result, err := s.command(ctx, req.Msg.ItemId, coordinator.CommandBlockerClear, 0, func(c *coordinator.ManifestCommand) {
		if !req.Msg.ClearAll {
			c.BlockerID = req.Msg.BlockerId
		}
	})
	return response(&v1.ClearBlockerResponse{Item: item(result.Manifest, result.Revision)}, err)
}
func note(n itemnotes.Note) *v1.Note {
	out := &v1.Note{Time: stamp(n.Time), Text: n.Text, Head: n.Head}
	if n.Attachment != nil {
		a := n.Attachment
		out.Attachment = &v1.Attachment{Name: a.Name, Sha256: a.SHA256, Size: uint32(a.Size), ContentType: a.Type, Path: a.Path}
	}
	return out
}
func (s *Server) AddNote(ctx context.Context, req *connect.Request[v1.AddNoteRequest]) (*connect.Response[v1.AddNoteResponse], error) {
	n, err := s.Local.AddNote(ctx, coordinator.AddNoteRequest{ItemID: req.Msg.ItemId, Text: req.Msg.Text})
	return response(&v1.AddNoteResponse{Note: note(n)}, err)
}
func (s *Server) Attach(ctx context.Context, req *connect.Request[v1.AttachRequest]) (*connect.Response[v1.AttachResponse], error) {
	if req.Msg.CheckoutPath != "" {
		c, err := s.canonical(ctx, req.Msg.ItemId, 0)
		if err != nil {
			return nil, rpcError(err)
		}
		if c.Manifest.Checkout.Claim == nil {
			return nil, invalid("checkout_path requires a current workspace claim")
		}
	}
	n, err := s.Local.Attach(ctx, coordinator.AttachRequest{ItemID: req.Msg.ItemId, Name: req.Msg.Name, Content: req.Msg.Content, Text: req.Msg.Text, Path: req.Msg.CheckoutPath})
	return response(&v1.AttachResponse{Note: note(n)}, err)
}
func (s *Server) ListNotes(ctx context.Context, req *connect.Request[v1.ListNotesRequest]) (*connect.Response[v1.ListNotesResponse], error) {
	if _, err := s.canonical(ctx, req.Msg.ItemId, 0); err != nil {
		return nil, rpcError(err)
	}
	notes, err := itemnotes.Read(s.App.Store.ItemDir(req.Msg.ItemId))
	out := &v1.ListNotesResponse{Notes: []*v1.Note{}, Attachments: []*v1.CurrentAttachment{}}
	for _, n := range notes {
		out.Notes = append(out.Notes, note(n))
	}
	for _, a := range itemnotes.Attachments(notes) {
		out.Attachments = append(out.Attachments, &v1.CurrentAttachment{Attachment: note(itemnotes.Note{Attachment: &a.Attachment}).Attachment, Time: stamp(a.Time)})
	}
	return response(out, err)
}
func (s *Server) ReadAttachment(ctx context.Context, req *connect.Request[v1.ReadAttachmentRequest], stream *connect.ServerStream[v1.ReadAttachmentResponse]) error {
	notes, err := s.ListNotes(ctx, connect.NewRequest(&v1.ListNotesRequest{ItemId: req.Msg.ItemId}))
	if err != nil {
		return err
	}
	var meta *v1.Attachment
	for _, n := range notes.Msg.Notes {
		if a := n.Attachment; a != nil && a.Sha256 == req.Msg.Sha256 {
			meta = a
		}
	}
	if meta == nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("attachment not found"))
	}
	file, err := itemnotes.OpenBlob(s.App.Store.ItemDir(req.Msg.ItemId), req.Msg.Sha256)
	if err != nil {
		return rpcError(err)
	}
	defer file.Close()
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.Read(buffer)
		if n > 0 || meta != nil {
			if send := stream.Send(&v1.ReadAttachmentResponse{Attachment: meta, Chunk: buffer[:n]}); send != nil {
				return send
			}
			meta = nil
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
