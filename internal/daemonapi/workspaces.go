package daemonapi

import (
	"context"
	"os"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func (s *Server) requireActive(ctx context.Context, itemID string) (model.Manifest, error) {
	c, err := s.canonical(ctx, itemID, 0)
	if err != nil {
		return model.Manifest{}, err
	}
	if c.Manifest.State != model.StateActive {
		return c.Manifest, precondition("item must be active")
	}
	return c.Manifest, nil
}
func (s *Server) requireStopped(ctx context.Context, itemID string) error {
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(itemID))
	if err != nil {
		return err
	}
	if runtime.Online {
		return precondition("agent is still running")
	}
	return nil
}

// setupBlocksAgent reports whether the current claim's setup keeps an agent
// from starting: one still running or one that failed. A claim with no setup
// record predates setup, or skipped it, and is ready.
func setupBlocksAgent(setup *v1.SetupRun) bool {
	return setup != nil && setup.State != v1.SetupState_SETUP_STATE_SUCCEEDED
}

func (s *Server) setup(ctx context.Context, itemID string) *v1.SetupRun {
	status, err := s.App.WorkspaceSetupStatus(ctx, resolve(itemID))
	if err != nil || !status.Current || status.Run == nil {
		return nil
	}
	r := status.Run
	out := &v1.SetupRun{ClaimId: r.ClaimID, StartedAt: stamp(r.StartedAt), FinishedAt: optionalStamp(r.FinishedAt), Error: r.Error, Warnings: r.Warnings}
	switch r.Status {
	case app.SetupRunning:
		out.State = v1.SetupState_SETUP_STATE_RUNNING
	case app.SetupFailed:
		out.State = v1.SetupState_SETUP_STATE_FAILED
	default:
		out.State = v1.SetupState_SETUP_STATE_SUCCEEDED
	}
	if data, err := os.ReadFile(r.Log); err == nil {
		if len(data) > 64<<10 {
			data = data[len(data)-(64<<10):]
		}
		out.LogTail = string(data)
	}
	return out
}
func (s *Server) GetWorkspace(ctx context.Context, req *connect.Request[v1.GetWorkspaceRequest]) (*connect.Response[v1.GetWorkspaceResponse], error) {
	value, err := s.App.WorkspaceStatus(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.GetWorkspaceResponse{Checkout: checkout(value.Checkout), Setup: s.setup(ctx, req.Msg.ItemId), Git: &v1.GitState{Head: value.Git.CurrentHead, CurrentBranch: value.Git.CurrentBranch, ExpectedBranch: value.Git.ExpectedBranch, BranchMismatch: value.Git.BranchMismatch, Dirty: value.Git.Dirty}}
	out.Status = v1.WorktreeStatus_WORKTREE_STATUS_ABSENT
	if value.Checkout.Claim != nil {
		out.Status = v1.WorktreeStatus_WORKTREE_STATUS_CLEAN
		if value.Git.Dirty {
			out.Status = v1.WorktreeStatus_WORKTREE_STATUS_CHANGED
		}
		if value.Git.BranchMismatch {
			out.Status = v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM
		}
	}
	out.Reason = strings.Join(value.Warnings, "; ")
	shown, err := s.App.ShowWorkItem(ctx, resolve(req.Msg.ItemId))
	if err == nil && shown.Repository != nil {
		if shown.Repository.Ahead != nil {
			out.Git.Ahead = uint32(*shown.Repository.Ahead)
		}
		if shown.Repository.Behind != nil {
			out.Git.Behind = uint32(*shown.Repository.Behind)
		}
	}
	return response(out, nil)
}
func (s *Server) EnsureWorkspace(ctx context.Context, req *connect.Request[v1.EnsureWorkspaceRequest]) (*connect.Response[v1.EnsureWorkspaceResponse], error) {
	if _, err := s.requireActive(ctx, req.Msg.ItemId); err != nil {
		return nil, rpcError(err)
	}
	result, err := s.App.EnsureWorkItemWorkspace(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.EnsureWorkspaceResponse{Checkout: checkout(result.Checkout)}
	if req.Msg.SkipSetup {
		ctx = app.WithSkipSetup(ctx)
		_, err = s.App.SetupWorkspace(ctx, resolve(req.Msg.ItemId), false)
		return response(out, err)
	}
	out.Job = s.startJob("setup_workspace", req.Msg.ItemId, "", func(ctx context.Context, j *jobRun) error {
		unlock := s.lock(req.Msg.ItemId)
		defer unlock()
		if _, err := s.requireActive(ctx, req.Msg.ItemId); err != nil {
			return err
		}
		j.phase("setting up checkout")
		_, err := s.App.SetupWorkspace(ctx, resolve(req.Msg.ItemId), false)
		return partial(err, req.Msg.ItemId, "claimed workspace")
	})
	return response(out, nil)
}
func (s *Server) SetupWorkspace(ctx context.Context, req *connect.Request[v1.SetupWorkspaceRequest]) (*connect.Response[v1.SetupWorkspaceResponse], error) {
	m, err := s.requireActive(ctx, req.Msg.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	if m.Checkout.Claim == nil {
		return nil, precondition("item has no workspace claim")
	}
	if err := s.requireStopped(ctx, m.ID); err != nil {
		return nil, err
	}
	job := s.startJob("setup_workspace", m.ID, "", func(ctx context.Context, j *jobRun) error {
		unlock := s.lock(m.ID)
		defer unlock()
		current, err := s.requireActive(ctx, m.ID)
		if err != nil {
			return err
		}
		if current.Checkout.Claim == nil || current.Checkout.Claim.ID != m.Checkout.Claim.ID {
			return aborted("workspace claim changed before setup")
		}
		if err := s.requireStopped(ctx, m.ID); err != nil {
			return err
		}
		j.phase("setting up checkout")
		_, err = s.App.SetupWorkspace(ctx, resolve(m.ID), true)
		return err
	})
	return response(&v1.SetupWorkspaceResponse{Job: job}, nil)
}
func (s *Server) CommitWorkspace(ctx context.Context, req *connect.Request[v1.CommitWorkspaceRequest]) (*connect.Response[v1.CommitWorkspaceResponse], error) {
	commit, removed, err := s.App.CommitWorkItemWorkspace(ctx, resolve(req.Msg.ItemId), req.Msg.Message)
	return response(&v1.CommitWorkspaceResponse{Commit: commit, RemovedCopies: removed}, err)
}
func (s *Server) ReleaseWorkspace(ctx context.Context, req *connect.Request[v1.ReleaseWorkspaceRequest]) (*connect.Response[v1.ReleaseWorkspaceResponse], error) {
	if err := s.requireStopped(ctx, req.Msg.ItemId); err != nil {
		return nil, err
	}
	result, err := s.App.ReleaseWorkItemWorkspace(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, rpcError(err)
	}
	value, err := s.item(ctx, req.Msg.ItemId)
	return response(&v1.ReleaseWorkspaceResponse{Item: value, Warnings: result.Warnings}, err)
}
func (s *Server) GetUncommittedChanges(ctx context.Context, req *connect.Request[v1.GetUncommittedChangesRequest]) (*connect.Response[v1.GetUncommittedChangesResponse], error) {
	value, err := s.App.ItemUncommittedChanges(ctx, resolve(req.Msg.ItemId))
	return response(&v1.GetUncommittedChangesResponse{Files: value.Files, AttachedCopies: value.Attached}, err)
}
func (s *Server) SearchWorkspaceFiles(ctx context.Context, req *connect.Request[v1.SearchWorkspaceFilesRequest]) (*connect.Response[v1.SearchWorkspaceFilesResponse], error) {
	m, err := s.App.Store.LoadManifest(req.Msg.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	if m.Checkout.Claim == nil {
		return nil, precondition("item has no workspace claim")
	}
	paths, err := findFiles(ctx, m.Checkout.Claim.Path, req.Msg.Query)
	if req.Msg.Limit > 0 && len(paths) > int(req.Msg.Limit) {
		paths = paths[:req.Msg.Limit]
	}
	return response(&v1.SearchWorkspaceFilesResponse{Paths: paths}, err)
}
