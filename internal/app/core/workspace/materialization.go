package workspace

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type CheckoutCreateResult struct {
	WorkItemID string         `json:"work_item_id"`
	Checkout   model.Checkout `json:"checkout"`
	Source     string         `json:"source"`
	Manifest   model.Manifest `json:"manifest"`
	Warnings   []string       `json:"warnings"`
}

type CheckoutRemoveResult struct {
	WorkItemID string         `json:"work_item_id"`
	Checkout   model.Checkout `json:"checkout"`
	LastHead   string         `json:"last_head"`
	Dirty      bool           `json:"dirty"`
	Manifest   model.Manifest `json:"manifest"`
	Warnings   []string       `json:"warnings"`
}

type CheckoutStatusResult struct {
	WorkItemID      string         `json:"work_item_id"`
	Checkout        model.Checkout `json:"checkout"`
	CurrentHead     string         `json:"current_head"`
	ExpectedBranch  string         `json:"expected_branch,omitempty"`
	CurrentBranch   string         `json:"current_branch,omitempty"`
	BranchMatches   bool           `json:"branch_matches"`
	BranchMismatch  bool           `json:"branch_mismatch,omitempty"`
	Dirty           bool           `json:"dirty"`
	StatusPorcelain string         `json:"status_porcelain"`
	Warnings        []string       `json:"warnings"`
}

type EnsureResult struct {
	WorkItemID string
	Manifest   model.Manifest
	Warnings   []string
}

type ReleaseResult struct {
	WorkItemID string         `json:"work_item_id"`
	Changed    bool           `json:"changed"`
	Manifest   model.Manifest `json:"manifest"`
	Warnings   []string       `json:"warnings"`
}

func (s *Service) CheckoutCreate(ctx context.Context, opts contract.ResolveOptions) (CheckoutCreateResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return CheckoutCreateResult{}, err
	}
	if m.State == model.StateArchived {
		return CheckoutCreateResult{}, fmt.Errorf("work item %s is archived; run `wi state set backlog --item %s` before creating workspace resources", m.ID, m.ID)
	}
	warnings := []string{}
	if m.Checkout.Present() && m.Checkout.Path != nil && *m.Checkout.Path != "" {
		if _, err := os.Stat(*m.Checkout.Path); err == nil {
			if err := s.ensureCheckoutBranch(ctx, m); err != nil {
				return CheckoutCreateResult{}, err
			}
			return CheckoutCreateResult{WorkItemID: m.ID, Checkout: m.Checkout, Source: "already present", Manifest: m, Warnings: warnings}, nil
		} else if !os.IsNotExist(err) {
			return CheckoutCreateResult{}, fmt.Errorf("inspect checkout path: %w", err)
		} else {
			if m.Checkout.Kind == model.WorkspaceKindRepositoryHome {
				return CheckoutCreateResult{}, fmt.Errorf("repository home checkout path is unavailable: %s", *m.Checkout.Path)
			}
			warnings = append(warnings, "recorded checkout path is missing; assigning a slot")
		}
	}
	if m.Checkout.Kind == model.WorkspaceKindRepositoryHome {
		path := strings.TrimSpace(m.Repository.RootAtCreation)
		if path == "" {
			return CheckoutCreateResult{}, fmt.Errorf("repository-home checkout has no durable home path")
		}
		info, err := os.Stat(path)
		if err != nil {
			return CheckoutCreateResult{}, fmt.Errorf("repository home checkout path is unavailable: %w", err)
		}
		if !info.IsDir() {
			return CheckoutCreateResult{}, fmt.Errorf("repository home checkout path is not a directory: %s", path)
		}
		m.Checkout.Path = &path
		if err := s.ensureCheckoutBranch(ctx, m); err != nil {
			return CheckoutCreateResult{}, err
		}
		now := s.now()
		m.UpdatedAt = now
		if err := s.store.ClaimRepositoryHome(ctx, m); err != nil {
			return CheckoutCreateResult{}, err
		}
		if err := s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "checkout.assigned", "wi", map[string]any{"path": path, "source": "repository home", "branch": m.Checkout.Branch})); err != nil {
			warnings = append(warnings, "could not append checkout.assigned event: "+err.Error())
		}
		return CheckoutCreateResult{WorkItemID: m.ID, Checkout: m.Checkout, Source: "repository home", Manifest: m, Warnings: warnings}, nil
	}
	assigned, err := s.assignSlot(ctx, m)
	if err != nil {
		return CheckoutCreateResult{}, err
	}
	m, warnings = assigned.Manifest, append(warnings, assigned.Warnings...)
	now := s.now()
	m.UpdatedAt = now
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return CheckoutCreateResult{}, err
	}
	if err := s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "checkout.assigned", "wi", map[string]any{"path": *m.Checkout.Path, "source": assigned.Source, "branch": m.Checkout.Branch})); err != nil {
		warnings = append(warnings, "could not append checkout.assigned event: "+err.Error())
	}
	return CheckoutCreateResult{WorkItemID: m.ID, Checkout: m.Checkout, Source: assigned.Source, Manifest: m, Warnings: warnings}, nil
}

func (s *Service) CheckoutRemove(ctx context.Context, opts contract.ResolveOptions, force bool) (CheckoutRemoveResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return CheckoutRemoveResult{}, err
	}
	warnings := []string{}
	if !m.Checkout.Present() || m.Checkout.Path == nil || *m.Checkout.Path == "" {
		warnings = append(warnings, "checkout is already absent")
		return CheckoutRemoveResult{WorkItemID: m.ID, Checkout: m.Checkout, Manifest: m, Warnings: warnings}, nil
	}
	previousPath := *m.Checkout.Path
	released, err := s.releaseCheckout(ctx, m, force)
	if err != nil {
		return CheckoutRemoveResult{}, err
	}
	m, warnings = released.Manifest, append(warnings, released.Warnings...)
	now := s.now()
	m.UpdatedAt = now
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return CheckoutRemoveResult{}, err
	}
	if err := s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "checkout.released", "user", map[string]any{"path": previousPath, "last_head": released.LastHead, "slot": released.Slot})); err != nil {
		warnings = append(warnings, "could not append checkout.released event: "+err.Error())
	}
	return CheckoutRemoveResult{WorkItemID: m.ID, Checkout: m.Checkout, LastHead: released.LastHead, Dirty: released.Dirty, Manifest: m, Warnings: warnings}, nil
}

func (s *Service) CheckoutStatus(ctx context.Context, opts contract.ResolveOptions) (CheckoutStatusResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return CheckoutStatusResult{}, err
	}
	res := CheckoutStatusResult{WorkItemID: m.ID, Checkout: m.Checkout, ExpectedBranch: ExpectedBranch(m), Warnings: []string{}}
	if !m.Checkout.Present() || m.Checkout.Path == nil || *m.Checkout.Path == "" {
		res.Warnings = append(res.Warnings, "checkout is absent")
		return res, nil
	}
	if _, err := os.Stat(*m.Checkout.Path); err != nil {
		res.Warnings = append(res.Warnings, "checkout path is unavailable: "+err.Error())
		return res, nil
	}
	if branch, err := s.git.CurrentBranch(ctx, *m.Checkout.Path); err == nil {
		res.CurrentBranch, res.BranchMatches = branch, branch == res.ExpectedBranch
		res.BranchMismatch = res.ExpectedBranch != "" && branch != "" && branch != res.ExpectedBranch
		if res.BranchMismatch {
			res.Warnings = append(res.Warnings, fmt.Sprintf("checkout branch mismatch: expected %s, found %s", res.ExpectedBranch, branch))
		}
	} else {
		res.Warnings = append(res.Warnings, "could not inspect checkout branch: "+err.Error())
	}
	if head, err := s.git.Head(ctx, *m.Checkout.Path); err == nil {
		res.CurrentHead = head
	} else {
		res.Warnings = append(res.Warnings, "could not resolve checkout HEAD: "+err.Error())
	}
	if status, err := s.git.StatusPorcelain(ctx, *m.Checkout.Path); err == nil {
		res.StatusPorcelain, res.Dirty = status, strings.TrimSpace(status) != ""
	} else {
		res.Warnings = append(res.Warnings, "could not inspect checkout status: "+err.Error())
	}
	return res, nil
}

func (s *Service) Ensure(ctx context.Context, opts contract.ResolveOptions) (EnsureResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return EnsureResult{}, err
	}
	if m.State == model.StateArchived {
		return EnsureResult{}, fmt.Errorf("work item %s is archived; set it to backlog before ensuring a workspace", m.ID)
	}
	m, warnings, err := s.EnsureCheckout(ctx, opts, m, true)
	return EnsureResult{WorkItemID: m.ID, Manifest: m, Warnings: warnings}, err
}

func (s *Service) EnsureCheckout(ctx context.Context, opts contract.ResolveOptions, m model.Manifest, create bool) (model.Manifest, []string, error) {
	warnings := []string{}
	if !m.Checkout.Present() || m.Checkout.Path == nil || *m.Checkout.Path == "" {
		if !create {
			return model.Manifest{}, warnings, fmt.Errorf("checkout is absent; run `wi workspace ensure` first")
		}
		created, err := s.CheckoutCreate(ctx, opts)
		if err != nil {
			return model.Manifest{}, warnings, err
		}
		m, warnings = created.Manifest, append(warnings, created.Warnings...)
	} else if _, err := os.Stat(*m.Checkout.Path); err != nil {
		if !os.IsNotExist(err) {
			return model.Manifest{}, warnings, fmt.Errorf("checkout path is unavailable: %w", err)
		}
		if !create {
			return model.Manifest{}, warnings, fmt.Errorf("checkout path is missing; run `wi workspace ensure` first")
		}
		created, err := s.CheckoutCreate(ctx, opts)
		if err != nil {
			return model.Manifest{}, warnings, err
		}
		m, warnings = created.Manifest, append(warnings, created.Warnings...)
	}
	if err := s.ensureCheckoutBranch(ctx, m); err != nil {
		return model.Manifest{}, warnings, err
	}
	return m, warnings, nil
}

func (s *Service) EnsureUsable(m model.Manifest) error {
	if !m.Checkout.Present() || m.Checkout.Path == nil || *m.Checkout.Path == "" {
		return fmt.Errorf("checkout is absent; run `wi workspace ensure` first")
	}
	if _, err := os.Stat(*m.Checkout.Path); err != nil {
		return fmt.Errorf("checkout path is unavailable: %w", err)
	}
	return nil
}

func (s *Service) Release(ctx context.Context, opts contract.ResolveOptions, force bool) (ReleaseResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return ReleaseResult{}, err
	}
	if runtime, err := s.store.LoadAgentRuntime(m.ID); err != nil {
		return ReleaseResult{}, err
	} else if s.ownership != nil && s.ownership(runtime) {
		return ReleaseResult{}, fmt.Errorf("agent runtime %s is still active; run `wi agent runtime stop --item %s`, wait for it to exit, then release the workspace", runtime.ID, m.ID)
	}
	if s.tmux != nil && strings.TrimSpace(m.TerminalSessionName()) != "" {
		exists, err := s.tmux.HasSession(ctx, m.TerminalSessionName())
		if err != nil {
			return ReleaseResult{}, fmt.Errorf("could not inspect optional terminal before releasing workspace: %w", err)
		}
		if exists {
			return ReleaseResult{}, fmt.Errorf("terminal session %s is still present; run `wi terminal close --item %s` before releasing the workspace", m.TerminalSessionName(), m.ID)
		}
	}
	if err := s.EnsureReleasable(ctx, m); err != nil {
		return ReleaseResult{}, err
	}
	hadCheckout := m.Checkout.Present() && m.Checkout.Path != nil && *m.Checkout.Path != ""
	released, err := s.releaseCheckout(ctx, m, force)
	if err != nil {
		return ReleaseResult{}, err
	}
	closed := released.Manifest
	if hadCheckout {
		now := s.now()
		closed.UpdatedAt = now
		if err := s.store.SaveManifest(ctx, closed); err != nil {
			return ReleaseResult{}, err
		}
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "workspace.released", "user", map[string]any{"state": m.State, "forced": force}))
	}
	return ReleaseResult{WorkItemID: m.ID, Changed: hadCheckout, Manifest: closed, Warnings: released.Warnings}, nil
}

func (s *Service) EnsureReleasable(ctx context.Context, m model.Manifest) error {
	if !m.Checkout.Present() || m.Checkout.Path == nil || *m.Checkout.Path == "" {
		return nil
	}
	path := *m.Checkout.Path
	if m.Checkout.Kind != model.WorkspaceKindRepositoryHome {
		if err := s.validateManagedCheckoutPath(path); err != nil {
			return err
		}
	}
	if err := s.ensureCheckoutBranch(ctx, m); err != nil {
		return err
	}
	status, err := s.git.StatusPorcelain(ctx, path)
	if err != nil {
		return fmt.Errorf("could not inspect checkout status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("checkout has uncommitted changes; commit, stash, or clean before closing the workspace")
	}
	return nil
}

func (s *Service) ReleaseCheckout(ctx context.Context, m model.Manifest, force bool) (CheckoutRelease, error) {
	return s.releaseCheckout(ctx, m, force)
}

func (s *Service) EnsureBranch(ctx context.Context, m model.Manifest) error {
	return s.ensureCheckoutBranch(ctx, m)
}
