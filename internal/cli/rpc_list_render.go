package cli

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	"github.com/regb/workitem/internal/app"
	view "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/config"
)

func rpcRenderList(ctx context.Context, cfg Config, value *v1.ListItemsResponse, mode string, ids, noAgent bool) error {
	result, agents, err := rpcWorkList(ctx, cfg, value, mode, noAgent)
	if err != nil {
		return err
	}
	current := ""
	if selected, e := rpcSelect(ctx, cfg, ""); e == nil {
		current = selected
	}
	for _, warning := range value.Warnings {
		fmt.Fprintln(cfg.Stderr, "warning:", warning)
	}
	printWorkList(cfg.Stdout, result, mode, colorEnabled(cfg.Env, cfg.Stdout), agents, listDisplayOptions{ShowIDs: ids || mode != "active", CurrentID: current})
	return nil
}

// rpcWorkList turns an API list into the work-list presentation model that
// the list and the picker render, with each active item's agent status.
func rpcWorkList(ctx context.Context, cfg Config, value *v1.ListItemsResponse, mode string, noAgent bool) (app.WorkListResult, map[string]string, error) {
	result := app.WorkListResult{}
	result.DeepWork.Limit = config.Default().DeepWork.MaxActive
	if cfg.GlobalConfig != nil {
		result.DeepWork.Limit = cfg.GlobalConfig.DeepWork.MaxActive
	}
	active, err := cfg.API.Items.ListItems(ctx, rpcRequest(cfg, &v1.ListItemsRequest{States: []v1.ItemState{v1.ItemState_ITEM_STATE_ACTIVE}}))
	if err != nil {
		return result, nil, err
	}
	for _, row := range active.Msg.Items {
		if row.DeepWork {
			result.DeepWork.Active++
		}
	}
	names := map[string]string{}
	repositories, err := cfg.API.Repositories.ListRepositories(ctx, rpcRequest(cfg, &v1.ListRepositoriesRequest{IncludeArchived: true}))
	if err != nil {
		return result, nil, err
	}
	for _, repo := range repositories.Msg.Repositories {
		names[repo.Id] = repo.Name
	}
	agents := map[string]string{}
	for i, row := range value.Items {
		blockers := apiview.Blockers(row.Blockers)
		waiting := false
		for _, blocker := range blockers {
			if blocker.Open(time.Now()) {
				waiting = true
			}
		}
		item := app.WorkListItem{ID: row.Id, Slug: row.Slug, Title: row.Title, State: apiview.State(row.State), Resolution: apiview.Resolution(row.Resolution), DeepWork: row.DeepWork, Labels: row.Labels, RepositoryID: row.RepositoryId, Repository: names[row.RepositoryId], Blockers: blockers, Waiting: waiting, UpdatedAt: apiview.Time(row.UpdatedAt)}
		if item.Repository == "" {
			item.Repository = row.RepositoryId
		}
		item.CapacityFull = item.DeepWork && item.State == "backlog" && result.DeepWork.Active >= result.DeepWork.Limit
		if o := row.Observation; o != nil {
			item.Unread = o.Unread
			if o.NeedsAttention {
				item.AttentionRank = i + 1
			}
			if !noAgent && mode != "archived" && item.State == "active" {
				status := apiview.Status(o.AgentStatus)
				agents[item.ID] = status
				bucket := agentListBucket(status)
				if o.WorktreeStatus == v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM {
					bucket = "needs_fixing"
				}
				item.Agent = &view.AgentStatus{Status: status, Label: status, Bucket: bucket, Reason: o.AgentReason}
				item.Worktree = &view.WorktreeStatus{Status: apiview.Worktree(o.WorktreeStatus), Dirty: o.WorktreeDirty, Reason: o.WorktreeReason, BranchMismatch: o.BranchMismatch}
			}
		}
		switch item.State {
		case "closed":
			result.Sections.Closed = append(result.Sections.Closed, item)
		case "backlog":
			result.Sections.Backlog = append(result.Sections.Backlog, item)
		case "active":
			if waiting {
				result.Sections.Waiting = append(result.Sections.Waiting, item)
			} else {
				result.Sections.Active = append(result.Sections.Active, item)
			}
		}
	}
	return result, agents, nil
}
