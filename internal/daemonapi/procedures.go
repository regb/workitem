package daemonapi

import (
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	c "github.com/regb/workitem/gen/wi/core/v1/corev1connect"
	"github.com/regb/workitem/internal/model"
)

// Every procedure says, in procedures, how the interceptor treats it: which
// item and repository its request names, and what it serializes on while it
// runs. Nothing is inferred from field or method names: a new procedure is
// described here, or the interceptor refuses it and a test fails. Access is
// described beside it, in access.go.

// lockScope is what a call serializes on while it runs. Streams take none.
type lockScope uint8

const (
	lockNone       lockScope = iota
	lockItem                 // the item the request names
	lockRepository           // the repository the request names
	lockProfiles             // the agent profiles
)

// procedure describes a procedure for the interceptor.
type procedure struct {
	// item reads the item the request names; nil when it names none.
	item func(any) string
	// optionalItem lets the request name no item: it then acts on none, or
	// on one its other fields name and the handler checks.
	optionalItem bool
	// repository reads the repository the request names; nil when it
	// names none.
	repository func(any) string
	lock       lockScope
	// lockIf, when set, takes the lock only for requests it accepts.
	lockIf func(any) bool
}

func itemField(m any) string { return m.(interface{ GetItemId() string }).GetItemId() }
func repositoryField(m any) string {
	return m.(interface{ GetRepositoryId() string }).GetRepositoryId()
}

// comparisonItem is the item a revision read names in its comparison.
func comparisonItem(m any) string {
	return m.(interface {
		GetComparison() *v1.GetRevisionReviewRequest
	}).GetComparison().GetItemId()
}

// createdRepository is the repository a create-and-start names in its item.
func createdRepository(m any) string {
	return m.(*v1.CreateAndStartItemRequest).GetItem().GetRepositoryId()
}

var (
	noItem     = procedure{}
	readItem   = procedure{item: itemField}
	changeItem = procedure{item: itemField, lock: lockItem}
	readRepo   = procedure{repository: repositoryField}
	changeRepo = procedure{repository: repositoryField, lock: lockRepository}
	// Review records keep their own versions and fences: their changes do
	// not hold the item, so a long capture does not block the agent.
	reviewChange   = readItem
	changeProfiles = procedure{lock: lockProfiles}
)

var procedures = map[string]procedure{
	// Items.
	c.ItemServiceCreateItemProcedure:     changeRepo,
	c.ItemServiceGetItemProcedure:        readItem,
	c.ItemServiceResolveItemProcedure:    noItem,
	c.ItemServiceListItemsProcedure:      noItem,
	c.ItemServiceSearchItemsProcedure:    noItem,
	c.ItemServiceUpdateItemProcedure:     changeItem,
	c.ItemServiceSetItemStateProcedure:   changeItem,
	c.ItemServiceDeleteItemProcedure:     changeItem,
	c.ItemServiceMarkReadProcedure:       changeItem,
	c.ItemServiceDeferItemProcedure:      changeItem,
	c.ItemServiceAddBlockerProcedure:     changeItem,
	c.ItemServiceClearBlockerProcedure:   changeItem,
	c.ItemServiceListItemEventsProcedure: readItem,
	c.ItemServiceAddNoteProcedure:        changeItem,
	c.ItemServiceAttachProcedure:         changeItem,
	c.ItemServiceListNotesProcedure:      readItem,
	c.ItemServiceReadAttachmentProcedure: readItem,
	c.ItemServiceListAccessProcedure:     noItem,
	c.ItemServiceGrantAccessProcedure:    noItem,
	c.ItemServiceRevokeAccessProcedure:   noItem,

	// Lifecycle. A create-and-start holds its repository while it creates,
	// as a creation does.
	c.LifecycleServiceCreateAndStartItemProcedure: {repository: createdRepository, lock: lockRepository},
	c.LifecycleServiceStartItemProcedure:          changeItem,
	c.LifecycleServiceShelveItemProcedure:         changeItem,
	c.LifecycleServicePlanCloseProcedure:          readItem,
	c.LifecycleServiceCloseItemProcedure:          changeItem,
	c.LifecycleServiceReopenItemProcedure:         changeItem,

	// Workspaces.
	c.WorkspaceServiceGetWorkspaceProcedure:          readItem,
	c.WorkspaceServiceEnsureWorkspaceProcedure:       changeItem,
	c.WorkspaceServiceSetupWorkspaceProcedure:        changeItem,
	c.WorkspaceServiceCommitWorkspaceProcedure:       changeItem,
	c.WorkspaceServiceReleaseWorkspaceProcedure:      changeItem,
	c.WorkspaceServiceGetUncommittedChangesProcedure: readItem,
	c.WorkspaceServiceSearchWorkspaceFilesProcedure:  readItem,
	c.WorkspaceServiceListWorkspaceFilesProcedure:    readItem,
	c.WorkspaceServiceGetWorkspaceFileProcedure:      readItem,
	c.WorkspaceServiceGetWorkspaceDocumentProcedure:  readItem,
	c.WorkspaceServiceSaveWorkspaceFileProcedure:     changeItem,

	// Agents.
	c.AgentServiceGetAgentProcedure:            readItem,
	c.AgentServiceStartAgentProcedure:          changeItem,
	c.AgentServiceStopAgentProcedure:           changeItem,
	c.AgentServiceRestartAgentProcedure:        changeItem,
	c.AgentServiceSendMessageProcedure:         changeItem,
	c.AgentServiceAbortTurnProcedure:           changeItem,
	c.AgentServiceAnswerQuestionProcedure:      changeItem,
	c.AgentServiceGetAgentSettingsProcedure:    readItem,
	c.AgentServiceUpdateAgentSettingsProcedure: changeItem,
	c.AgentServiceSuggestTitleProcedure:        readItem,
	c.AgentServiceGetHistoryProcedure:          readItem,
	c.AgentServiceGetSubagentHistoryProcedure:  readItem,
	c.AgentServiceWatchLiveContentProcedure:    readItem,

	// Control. A lease may be named by its ID instead of its item.
	c.ControlServiceGetControlProcedure:     readItem,
	c.ControlServiceAcquireControlProcedure: changeItem,
	c.ControlServiceRenewControlProcedure:   noItem,
	c.ControlServiceReleaseControlProcedure: {item: itemField, optionalItem: true, lock: lockItem},

	// Events and jobs.
	c.EventServiceWatchProcedure:        readItem,
	c.EventServiceWatchChangesProcedure: noItem,
	c.JobServiceGetJobProcedure:         noItem,
	c.JobServiceListJobsProcedure:       {item: itemField, optionalItem: true},
	c.JobServiceWatchJobProcedure:       noItem,
	c.JobServiceAnswerApprovalProcedure: noItem,
	c.JobServiceCancelJobProcedure:      noItem,

	// Reviews.
	c.ReviewServiceListReviewRevisionsProcedure:   readItem,
	c.ReviewServiceCaptureReviewRevisionProcedure: reviewChange,
	c.ReviewServiceGetRevisionReviewProcedure:     readItem,
	c.ReviewServiceGetRevisionPatchProcedure:      {item: comparisonItem},
	c.ReviewServiceGetRevisionReviewFileProcedure: {item: comparisonItem},
	c.ReviewServiceGetReviewThreadsProcedure:      readItem,
	c.ReviewServiceGetReviewDraftsProcedure:       readItem,
	c.ReviewServiceSaveReviewDraftProcedure:       reviewChange,
	c.ReviewServiceDeleteReviewDraftsProcedure:    reviewChange,
	c.ReviewServicePublishReviewRoundProcedure:    reviewChange,
	c.ReviewServiceReplyReviewThreadsProcedure:    reviewChange,
	c.ReviewServiceGetReviewIntentProcedure:       readItem,
	c.ReviewServiceSetReviewIntentProcedure:       reviewChange,
	c.ReviewServiceNotifyReviewRoundProcedure:     reviewChange,
	c.ReviewServiceGetReviewProcedure:             readItem,
	c.ReviewServiceGetReviewFileProcedure:         readItem,
	c.ReviewServiceListReviewCommitsProcedure:     readItem,

	// Repositories. A status that fetches changes the store.
	c.RepositoryServiceListRepositoriesProcedure:  noItem,
	c.RepositoryServiceGetRepositoryProcedure:     readRepo,
	c.RepositoryServiceAddRepositoryProcedure:     noItem,
	c.RepositoryServiceRenameRepositoryProcedure:  changeRepo,
	c.RepositoryServiceArchiveRepositoryProcedure: changeRepo,
	c.RepositoryServiceRestoreRepositoryProcedure: changeRepo,
	c.RepositoryServiceGetRepositoryStatusProcedure: {repository: repositoryField, lock: lockRepository, lockIf: func(m any) bool {
		return m.(*v1.GetRepositoryStatusRequest).GetFetch()
	}},
	c.RepositoryServiceSetUpstreamProcedure:            changeRepo,
	c.RepositoryServiceSyncRepositoryProcedure:         changeRepo,
	c.RepositoryServiceIntegrateItemProcedure:          changeItem,
	c.RepositoryServiceGetRepositorySettingsProcedure:  readRepo,
	c.RepositoryServiceSetRepositorySettingProcedure:   changeRepo,
	c.RepositoryServiceUnsetRepositorySettingProcedure: changeRepo,
	c.RepositoryServiceGetCheckoutSetupProcedure:       readRepo,
	c.RepositoryServiceUpdateCheckoutSetupProcedure:    changeRepo,
	c.RepositoryServiceListRepositoryFilesProcedure:    readRepo,
	c.RepositoryServiceSearchRepositoryFilesProcedure:  readRepo,
	c.RepositoryServiceReadRepositoryFileProcedure:     readRepo,
	c.RepositoryServicePutRepositoryFileProcedure:      changeRepo,
	c.RepositoryServiceDeleteRepositoryFileProcedure:   changeRepo,
	c.RepositoryServiceGetEnvironmentProcedure:         readRepo,
	c.RepositoryServiceUpdateEnvironmentProcedure:      changeRepo,

	// Agent profiles.
	c.AgentProfileServiceListAgentProfilesProcedure:      noItem,
	c.AgentProfileServicePutAgentProfileProcedure:        changeProfiles,
	c.AgentProfileServiceDeleteAgentProfileProcedure:     changeProfiles,
	c.AgentProfileServiceSetDefaultAgentProfileProcedure: changeProfiles,
	c.AgentProfileServiceGetProfileModelsProcedure:       noItem,
	c.AgentProfileServiceListDriversProcedure:            noItem,
	c.AgentProfileServiceSetTitleModelProcedure:          changeProfiles,

	// Schedules, the system and views.
	c.ScheduleServiceListSchedulesProcedure:   noItem,
	c.ScheduleServicePutScheduleProcedure:     noItem,
	c.ScheduleServiceDeleteScheduleProcedure:  noItem,
	c.ScheduleServiceRunScheduleProcedure:     noItem,
	c.SystemServiceGetInfoProcedure:           noItem,
	c.SystemServiceGetHealthProcedure:         noItem,
	c.SystemServiceRunDiagnosticsProcedure:    noItem,
	c.SystemServiceGetConfigProcedure:         noItem,
	c.SystemServiceListPluginsProcedure:       noItem,
	c.SystemServiceGetUsageProcedure:          noItem,
	c.SystemServiceShutdownProcedure:          noItem,
	c.SystemServiceRotateCredentialsProcedure: noItem,
	c.ViewServiceGetItemViewProcedure:         readItem,
}

// itemOf is the item a request names, checked; empty when the procedure
// names none, or the request leaves an optional one out.
func (p procedure) itemOf(message any) (string, error) {
	if p.item == nil {
		return "", nil
	}
	id := p.item(message)
	if id == "" && p.optionalItem {
		return "", nil
	}
	if !model.ValidID(id) {
		return "", invalid("valid item_id is required")
	}
	return id, nil
}

// repositoryOf is the repository a request names, checked.
func (p procedure) repositoryOf(message any) (string, error) {
	if p.repository == nil {
		return "", nil
	}
	id := p.repository(message)
	if !model.ValidID(id) {
		return "", invalid("valid repository_id is required")
	}
	return id, nil
}

// requestItem is the item a request to a procedure names, when it names a
// valid one.
func requestItem(procedure string, message any) string {
	id, err := procedures[procedure].itemOf(message)
	if err != nil {
		return ""
	}
	return id
}
