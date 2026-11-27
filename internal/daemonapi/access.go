package daemonapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/gen/wi/core/v1/corev1connect"
	"github.com/regb/workitem/internal/model"
)

// What an item's agent may call. Users (the operator, and wi web for its
// user) may call everything their credential's table allows; an item's
// agent is held to these lists. A test requires every procedure to be in
// exactly one of them.

// itemLevels are the calls on one item (their item_id) and the level an
// item's agent needs on that item.
var itemLevels = map[string]model.AccessLevel{
	corev1connect.AgentServiceGetAgentProcedure:                  model.AccessView,
	corev1connect.AgentServiceGetAgentSettingsProcedure:          model.AccessView,
	corev1connect.AgentServiceGetHistoryProcedure:                model.AccessView,
	corev1connect.AgentServiceGetSubagentHistoryProcedure:        model.AccessView,
	corev1connect.AgentServiceWatchLiveContentProcedure:          model.AccessView,
	corev1connect.ControlServiceGetControlProcedure:              model.AccessView,
	corev1connect.EventServiceWatchProcedure:                     model.AccessView,
	corev1connect.ItemServiceGetItemProcedure:                    model.AccessView,
	corev1connect.ItemServiceListItemEventsProcedure:             model.AccessView,
	corev1connect.ItemServiceListNotesProcedure:                  model.AccessView,
	corev1connect.ItemServiceReadAttachmentProcedure:             model.AccessView,
	corev1connect.JobServiceListJobsProcedure:                    model.AccessView,
	corev1connect.LifecycleServicePlanCloseProcedure:             model.AccessView,
	corev1connect.ReviewServiceGetReviewProcedure:                model.AccessView,
	corev1connect.ReviewServiceGetReviewDraftsProcedure:          model.AccessView,
	corev1connect.ReviewServiceGetReviewFileProcedure:            model.AccessView,
	corev1connect.ReviewServiceGetReviewIntentProcedure:          model.AccessView,
	corev1connect.ReviewServiceGetReviewThreadsProcedure:         model.AccessView,
	corev1connect.ReviewServiceGetRevisionReviewProcedure:        model.AccessView,
	corev1connect.ReviewServiceGetRevisionPatchProcedure:         model.AccessView,
	corev1connect.ReviewServiceGetRevisionReviewFileProcedure:    model.AccessView,
	corev1connect.ReviewServiceListReviewCommitsProcedure:        model.AccessView,
	corev1connect.ReviewServiceListReviewRevisionsProcedure:      model.AccessView,
	corev1connect.ViewServiceGetItemViewProcedure:                model.AccessView,
	corev1connect.WorkspaceServiceGetUncommittedChangesProcedure: model.AccessView,
	corev1connect.WorkspaceServiceGetWorkspaceProcedure:          model.AccessView,
	corev1connect.WorkspaceServiceGetWorkspaceDocumentProcedure:  model.AccessView,
	corev1connect.WorkspaceServiceGetWorkspaceFileProcedure:      model.AccessView,
	corev1connect.WorkspaceServiceListWorkspaceFilesProcedure:    model.AccessView,
	corev1connect.WorkspaceServiceSearchWorkspaceFilesProcedure:  model.AccessView,

	corev1connect.AgentServiceSendMessageProcedure:         model.AccessMessage,
	corev1connect.ItemServiceAddBlockerProcedure:           model.AccessMessage,
	corev1connect.ItemServiceAddNoteProcedure:              model.AccessMessage,
	corev1connect.ItemServiceAttachProcedure:               model.AccessMessage,
	corev1connect.ItemServiceClearBlockerProcedure:         model.AccessMessage,
	corev1connect.ItemServiceDeferItemProcedure:            model.AccessMessage,
	corev1connect.ItemServiceMarkReadProcedure:             model.AccessMessage,
	corev1connect.ReviewServiceDeleteReviewDraftsProcedure: model.AccessMessage,
	corev1connect.ReviewServiceNotifyReviewRoundProcedure:  model.AccessMessage,
	corev1connect.ReviewServiceReplyReviewThreadsProcedure: model.AccessMessage,
	corev1connect.ReviewServiceSaveReviewDraftProcedure:    model.AccessMessage,

	corev1connect.AgentServiceAbortTurnProcedure:             model.AccessControl,
	corev1connect.AgentServiceAnswerQuestionProcedure:        model.AccessControl,
	corev1connect.AgentServiceRestartAgentProcedure:          model.AccessControl,
	corev1connect.AgentServiceStartAgentProcedure:            model.AccessControl,
	corev1connect.AgentServiceStopAgentProcedure:             model.AccessControl,
	corev1connect.AgentServiceSuggestTitleProcedure:          model.AccessControl,
	corev1connect.AgentServiceUpdateAgentSettingsProcedure:   model.AccessControl,
	corev1connect.ControlServiceAcquireControlProcedure:      model.AccessControl,
	corev1connect.ControlServiceReleaseControlProcedure:      model.AccessControl,
	corev1connect.ItemServiceSetItemStateProcedure:           model.AccessControl,
	corev1connect.ItemServiceUpdateItemProcedure:             model.AccessControl,
	corev1connect.LifecycleServiceShelveItemProcedure:        model.AccessControl,
	corev1connect.LifecycleServiceStartItemProcedure:         model.AccessControl,
	corev1connect.WorkspaceServiceEnsureWorkspaceProcedure:   model.AccessControl,
	corev1connect.WorkspaceServiceReleaseWorkspaceProcedure:  model.AccessControl,
	corev1connect.WorkspaceServiceSaveWorkspaceFileProcedure: model.AccessControl,
	corev1connect.WorkspaceServiceSetupWorkspaceProcedure:    model.AccessControl,

	corev1connect.RepositoryServiceIntegrateItemProcedure:     model.AccessIntegrate,
	corev1connect.ReviewServiceCaptureReviewRevisionProcedure: model.AccessIntegrate,
	corev1connect.ReviewServicePublishReviewRoundProcedure:    model.AccessIntegrate,
	corev1connect.ReviewServiceSetReviewIntentProcedure:       model.AccessIntegrate,
	corev1connect.WorkspaceServiceCommitWorkspaceProcedure:    model.AccessIntegrate,

	corev1connect.LifecycleServiceCloseItemProcedure:  model.AccessManage,
	corev1connect.LifecycleServiceReopenItemProcedure: model.AccessManage,
}

// usersOnly are decisions a person makes: what agents may do, whose items
// are whose, what is filed or deleted, and the installation itself.
var usersOnly = map[string]string{
	corev1connect.ItemServiceDeleteItemProcedure:                     "deleting an item",
	corev1connect.ItemServiceListAccessProcedure:                     "reading access",
	corev1connect.ItemServiceCheckAccessProcedure:                    "reading access",
	corev1connect.WorkspaceServiceRunCommandProcedure:                "running a command as a person",
	corev1connect.ItemServiceListAccessRequestsProcedure:             "reading access",
	corev1connect.ItemServiceDismissAccessRequestProcedure:           "changing access",
	corev1connect.ItemServiceGrantAccessProcedure:                    "granting access",
	corev1connect.ItemServiceRevokeAccessProcedure:                   "revoking access",
	corev1connect.SystemServiceShutdownProcedure:                     "stopping the daemon",
	corev1connect.RepositoryServiceReadRepositoryFileProcedure:       "reading stored repository files, which may hold secrets",
	corev1connect.SystemServiceRotateCredentialsProcedure:            "rotating credentials",
	corev1connect.RepositoryServiceAddRepositoryProcedure:            "registering a repository",
	corev1connect.RepositoryServiceArchiveRepositoryProcedure:        "archiving a repository",
	corev1connect.RepositoryServiceRestoreRepositoryProcedure:        "restoring a repository",
	corev1connect.RepositoryServiceRenameRepositoryProcedure:         "renaming a repository",
	corev1connect.RepositoryServiceSetUpstreamProcedure:              "changing a repository's upstream",
	corev1connect.RepositoryServiceSetRepositorySettingProcedure:     "changing repository settings",
	corev1connect.RepositoryServiceUnsetRepositorySettingProcedure:   "changing repository settings",
	corev1connect.RepositoryServiceUpdateCheckoutSetupProcedure:      "changing checkout setup",
	corev1connect.RepositoryServiceUpdateEnvironmentProcedure:        "changing repository environment",
	corev1connect.RepositoryServicePutRepositoryFileProcedure:        "changing repository files",
	corev1connect.RepositoryServiceDeleteRepositoryFileProcedure:     "changing repository files",
	corev1connect.AgentProfileServiceDeleteAgentProfileProcedure:     "changing agent profiles",
	corev1connect.AgentProfileServicePutAgentProfileProcedure:        "changing agent profiles",
	corev1connect.AgentProfileServiceSetDefaultAgentProfileProcedure: "changing agent profiles",
	corev1connect.AgentProfileServiceSetTitleModelProcedure:          "changing agent profiles",
	corev1connect.ScheduleServicePutScheduleProcedure:                "changing schedules",
	corev1connect.ScheduleServiceDeleteScheduleProcedure:             "changing schedules",
	corev1connect.ScheduleServiceRunScheduleProcedure:                "running a schedule",
	corev1connect.JobServiceCancelJobProcedure:                       "cancelling a job",
}

// anyItem are calls on no one item that every item's agent may make.
// Creating items delegates them; lists show only what the agent may view.
var anyItem = map[string]bool{
	corev1connect.ItemServiceCreateItemProcedure:                  true,
	corev1connect.LifecycleServiceCreateAndStartItemProcedure:     true,
	corev1connect.ItemServiceListItemsProcedure:                   true,
	corev1connect.ItemServiceSearchItemsProcedure:                 true,
	corev1connect.ItemServiceResolveItemProcedure:                 true,
	corev1connect.EventServiceWatchChangesProcedure:               true,
	corev1connect.ControlServiceRenewControlProcedure:             true,
	corev1connect.JobServiceGetJobProcedure:                       true,
	corev1connect.JobServiceWatchJobProcedure:                     true,
	corev1connect.RepositoryServiceGetCheckoutSetupProcedure:      true,
	corev1connect.RepositoryServiceGetEnvironmentProcedure:        true,
	corev1connect.RepositoryServiceGetRepositoryProcedure:         true,
	corev1connect.RepositoryServiceGetRepositorySettingsProcedure: true,
	corev1connect.RepositoryServiceGetRepositoryStatusProcedure:   true,
	corev1connect.RepositoryServiceListRepositoriesProcedure:      true,
	corev1connect.RepositoryServiceListRepositoryFilesProcedure:   true,
	corev1connect.RepositoryServiceSearchRepositoryFilesProcedure: true,
	corev1connect.RepositoryServiceSyncRepositoryProcedure:        true,
	corev1connect.AgentProfileServiceGetProfileModelsProcedure:    true,
	corev1connect.AgentProfileServiceListAgentProfilesProcedure:   true,
	corev1connect.AgentProfileServiceListDriversProcedure:         true,
	corev1connect.ScheduleServiceListSchedulesProcedure:           true,
	corev1connect.SystemServiceGetConfigProcedure:                 true,
	corev1connect.SystemServiceGetHealthProcedure:                 true,
	corev1connect.SystemServiceGetInfoProcedure:                   true,
	corev1connect.SystemServiceGetUsageProcedure:                  true,
	corev1connect.SystemServiceListPluginsProcedure:               true,
	corev1connect.SystemServiceRunDiagnosticsProcedure:            true,
}

// refineForItems refuses what an item's agent may call but not ask for:
// secrets, and a repository reset to its upstream.
func refineForItems(who principal, message any) error {
	if !who.item() {
		return nil
	}
	refuse := func(what string) error {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s is for a person, not an item's agent", what))
	}
	switch r := message.(type) {
	case *v1.GetConfigRequest:
		if r.ShowSensitive {
			return refuse("reading secret configuration")
		}
	case *v1.GetRepositorySettingsRequest:
		if r.ShowSensitive {
			return refuse("reading secret repository settings")
		}
	case *v1.SyncRepositoryRequest:
		if r.Mode == v1.SyncMode_SYNC_MODE_RESET_TO_UPSTREAM {
			return refuse("resetting a repository to its upstream")
		}
	}
	return nil
}

// authorize holds an item's agent to the lists above. itemID is the item
// the call names, if any. Users pass.
func (s *Server) authorize(ctx context.Context, procedure, itemID string) error {
	who := caller(ctx).principal
	if !who.item() {
		return nil
	}
	if what, ok := usersOnly[procedure]; ok {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s is for a person, not an item's agent", what))
	}
	needed, onItem := itemLevels[procedure]
	if !onItem {
		if anyItem[procedure] {
			return nil
		}
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("an item's agent may not call %s", procedure))
	}
	if itemID == "" {
		// Only a call whose item is optional may name none: it then acts
		// on no item, or on one its other fields name and check.
		if procedures[procedure].optionalItem {
			return nil
		}
		return invalid("valid item_id is required")
	}
	has, err := s.itemAccess(ctx, who.String(), itemID)
	if err != nil {
		return err
	}
	if has >= needed {
		return nil
	}
	// A person may grant what was refused: it waits for them as a request.
	s.accessRequests.record(who.String(), itemID, procedure, needed, has)
	refusal := connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s has %s access to item %s and needs %s; a user can grant it with: wi access grant --item %s --on %s --level %s", who, has, itemID, needed, who.itemID, itemID, needed))
	if detail, detailErr := connect.NewErrorDetail(&v1.AccessDenied{Subject: who.String(), ItemId: itemID, Needed: v1.AccessLevel(needed), Has: v1.AccessLevel(has)}); detailErr == nil {
		refusal.AddDetail(detail)
	}
	return refusal
}

// itemAccess is the level subject has on an item.
func (s *Server) itemAccess(ctx context.Context, subject, itemID string) (model.AccessLevel, error) {
	target, err := s.Local.CanonicalManifest(ctx, itemID)
	if err != nil {
		return model.AccessNone, rpcError(err)
	}
	entries, err := s.Core.Database().AccessEntries()
	if err != nil {
		return model.AccessNone, rpcError(err)
	}
	return model.ItemAccess(subject, target.Manifest, entries, s.manifestLookup(ctx)), nil
}

// visibleTo filters items to those the caller may view: all of them for a
// user, those an item's agent has view access to.
func (s *Server) visibleTo(ctx context.Context) (func(model.Manifest) bool, error) {
	who := caller(ctx).principal
	if !who.item() {
		return func(model.Manifest) bool { return true }, nil
	}
	entries, err := s.Core.Database().AccessEntries()
	if err != nil {
		return nil, rpcError(err)
	}
	subject := who.String()
	lookup := s.manifestLookup(ctx)
	return func(m model.Manifest) bool { return model.ItemAccess(subject, m, entries, lookup) >= model.AccessView }, nil
}

func (s *Server) ListAccess(ctx context.Context, req *connect.Request[v1.ListAccessRequest]) (*connect.Response[v1.ListAccessResponse], error) {
	entries, err := s.Core.Database().AccessEntries()
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.ListAccessResponse{Entries: []*v1.AccessEntry{}}
	for _, entry := range entries {
		out.Entries = append(out.Entries, accessEntry(entry))
	}
	return response(out, nil)
}

func (s *Server) GrantAccess(ctx context.Context, req *connect.Request[v1.GrantAccessRequest]) (*connect.Response[v1.GrantAccessResponse], error) {
	r := req.Msg
	on := strings.TrimSpace(r.OnItemId)
	if on == "" {
		return nil, invalid("on_item_id is required: an item, or * for every item")
	}
	if on != model.EveryItem {
		if _, err := s.canonical(ctx, on, 0); err != nil {
			return nil, rpcError(err)
		}
	}
	if id := model.PrincipalItem(r.Subject); id != "" {
		if _, err := s.canonical(ctx, id, 0); err != nil {
			return nil, rpcError(err)
		}
	}
	entry, err := s.Core.Database().GrantAccess(model.AccessEntry{Subject: strings.TrimSpace(r.Subject), ItemID: on, Level: model.AccessLevel(r.Level), GrantedBy: model.ActorFrom(ctx), GrantedAt: time.Now().UTC()})
	if err != nil {
		return nil, rpcError(err)
	}
	return response(&v1.GrantAccessResponse{Entry: accessEntry(entry)}, nil)
}

func (s *Server) RevokeAccess(ctx context.Context, req *connect.Request[v1.RevokeAccessRequest]) (*connect.Response[v1.RevokeAccessResponse], error) {
	if strings.TrimSpace(req.Msg.EntryId) == "" {
		return nil, invalid("entry_id is required")
	}
	if err := s.Core.Database().RevokeAccess(req.Msg.EntryId); err != nil {
		return nil, rpcError(err)
	}
	return response(&v1.RevokeAccessResponse{}, nil)
}

func accessEntry(entry model.AccessEntry) *v1.AccessEntry {
	return &v1.AccessEntry{Id: entry.ID, Subject: entry.Subject, ItemId: entry.ItemID, Level: v1.AccessLevel(entry.Level), GrantedBy: entry.GrantedBy, GrantedAt: stamp(entry.GrantedAt)}
}

// viewable keeps the items the caller may view, in order.
func (s *Server) viewable(ctx context.Context, itemIDs []string) []string {
	who := caller(ctx).principal
	if !who.item() || len(itemIDs) == 0 {
		return itemIDs
	}
	out := []string{}
	for _, id := range itemIDs {
		if level, err := s.itemAccess(ctx, who.String(), id); err == nil && level >= model.AccessView {
			out = append(out, id)
		}
	}
	return out
}

// viewableJobs keeps the jobs the caller may see: those of items it may
// view, and for an item's agent no job that names no item.
func (s *Server) viewableJobs(ctx context.Context, jobs []*v1.Job) []*v1.Job {
	if !caller(ctx).principal.item() {
		return jobs
	}
	ids := map[string]bool{}
	for _, id := range s.viewable(ctx, jobItems(jobs)) {
		ids[id] = true
	}
	out := []*v1.Job{}
	for _, job := range jobs {
		if ids[job.ItemId] {
			out = append(out, job)
		}
	}
	return out
}

func jobItems(jobs []*v1.Job) []string {
	out := []string{}
	for _, job := range jobs {
		if job.ItemId != "" {
			out = append(out, job.ItemId)
		}
	}
	return out
}

// CheckAccess says what an item's agent may do to each of the items asked
// about.
func (s *Server) CheckAccess(ctx context.Context, req *connect.Request[v1.CheckAccessRequest]) (*connect.Response[v1.CheckAccessResponse], error) {
	subject := strings.TrimSpace(req.Msg.Subject)
	if model.PrincipalItem(subject) == "" {
		return nil, invalid("subject must be an item's agent, item:<id>")
	}
	if len(req.Msg.ItemIds) > 200 {
		return nil, invalid("at most 200 items at a time")
	}
	out := &v1.CheckAccessResponse{Access: []*v1.ItemAccess{}}
	for _, id := range req.Msg.ItemIds {
		if !model.ValidID(id) {
			return nil, invalid("valid item_ids are required")
		}
		level, err := s.itemAccess(ctx, subject, id)
		if err != nil {
			return nil, err
		}
		out.Access = append(out.Access, &v1.ItemAccess{ItemId: id, Level: v1.AccessLevel(level)})
	}
	return response(out, nil)
}

// mentionPattern finds the items a message mentions, as the web writes them:
// [title](wi:<id>) or [title](wi:<id> "slug").
var mentionPattern = regexp.MustCompile(`\]\(wi:([0-9A-HJKMNP-TV-Z]{26})(?: "[^"]*")?\)`)

// grantMentioned lets an item's agent read the items a person's message to
// it mentions: its item gains view access to each it could not view. A
// level it has is never lowered, and an agent's message grants nothing.
func (s *Server) grantMentioned(ctx context.Context, itemID, text string) {
	actor := model.ActorFrom(ctx)
	if actor == "" || model.PrincipalItem(actor) != "" {
		return
	}
	subject := model.ItemPrincipal(itemID)
	seen := map[string]bool{itemID: true}
	for _, match := range mentionPattern.FindAllStringSubmatch(text, 64) {
		mentioned := match[1]
		if seen[mentioned] {
			continue
		}
		seen[mentioned] = true
		level, err := s.itemAccess(ctx, subject, mentioned)
		if err != nil || level >= model.AccessView {
			continue
		}
		_, _ = s.Core.Database().GrantAccess(model.AccessEntry{Subject: subject, ItemID: mentioned, Level: model.AccessView, GrantedBy: actor, GrantedAt: time.Now().UTC()})
	}
}
