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
	if err := validateCheckoutKind(m.Checkout); err != nil {
		return CheckoutCreateResult{}, err
	}
	if executor := m.Checkout.Specification.Executor; executor != "" && executor != model.ExecutorLocal {
		return CheckoutCreateResult{}, fmt.Errorf("checkout executor %q is not supported by this local build", executor)
	}
	if m.State == model.StateClosed {
		return CheckoutCreateResult{}, fmt.Errorf("work item %s is closed; run `wi state set backlog --item %s` before creating workspace resources", m.ID, m.ID)
	}
	warnings := []string{}
	if m.Checkout.Present() && m.Checkout.Claim != nil && m.Checkout.Claim.Path != "" {
		if _, err := os.Stat(m.Checkout.Claim.Path); err == nil {
			if err := s.ensureCheckoutBranch(ctx, m); err != nil {
				warnings = append(warnings, err.Error())
			}
			return CheckoutCreateResult{WorkItemID: m.ID, Checkout: m.Checkout, Source: "already present", Manifest: m, Warnings: warnings}, nil
		} else if !os.IsNotExist(err) {
			return CheckoutCreateResult{}, fmt.Errorf("inspect checkout path: %w", err)
		} else {
			warnings = append(warnings, "recorded checkout path is missing; assigning a new claim")
			m.Checkout.ClearClaim()
		}
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
	if err := s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "checkout.assigned", "wi", map[string]any{"claim_id": m.Checkout.Claim.ID, "claim_generation": m.Checkout.Claim.Generation, "executor": m.Checkout.Claim.Executor, "path": m.Checkout.Claim.Path, "source": assigned.Source, "branch": m.Checkout.Specification.Branch})); err != nil {
		warnings = append(warnings, "could not append checkout.assigned event: "+err.Error())
	}
	return CheckoutCreateResult{WorkItemID: m.ID, Checkout: m.Checkout, Source: assigned.Source, Manifest: m, Warnings: warnings}, nil
}

func (s *Service) Ensure(ctx context.Context, opts contract.ResolveOptions) (EnsureResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return EnsureResult{}, err
	}
	if m.State == model.StateClosed {
		return EnsureResult{}, fmt.Errorf("work item %s is closed; set it to backlog before ensuring a workspace", m.ID)
	}
	m, warnings, err := s.EnsureCheckout(ctx, opts, m, true)
	return EnsureResult{WorkItemID: m.ID, Manifest: m, Warnings: warnings}, err
}

func (s *Service) EnsureCheckout(ctx context.Context, opts contract.ResolveOptions, m model.Manifest, create bool) (model.Manifest, []string, error) {
	if err := validateCheckoutKind(m.Checkout); err != nil {
		return model.Manifest{}, nil, err
	}
	if executor := m.Checkout.Specification.Executor; executor != "" && executor != model.ExecutorLocal {
		return model.Manifest{}, nil, fmt.Errorf("checkout executor %q is not supported by this local build", executor)
	}
	warnings := []string{}
	if !m.Checkout.Present() || m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" {
		if !create {
			return model.Manifest{}, warnings, fmt.Errorf("checkout is absent; run `wi workspace ensure` first")
		}
		created, err := s.CheckoutCreate(ctx, opts)
		if err != nil {
			return model.Manifest{}, warnings, err
		}
		m, warnings = created.Manifest, append(warnings, created.Warnings...)
	} else if _, err := os.Stat(m.Checkout.Claim.Path); err != nil {
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
		warnings = append(warnings, err.Error())
	}
	return m, warnings, nil
}

func (s *Service) Release(ctx context.Context, opts contract.ResolveOptions) (ReleaseResult, error) {
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
	hadCheckout := m.Checkout.Present() && m.Checkout.Claim != nil && m.Checkout.Claim.Path != ""
	var previousClaim *model.WorkspaceClaim
	if hadCheckout {
		claim := *m.Checkout.Claim
		previousClaim = &claim
	}
	released, err := s.releaseCheckout(ctx, m)
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
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEventFrom(ctx, now, "workspace.released", map[string]any{"claim_id": previousClaim.ID, "claim_generation": previousClaim.Generation, "executor": previousClaim.Executor, "state": m.State}))
	}
	return ReleaseResult{WorkItemID: m.ID, Changed: hadCheckout, Manifest: closed, Warnings: released.Warnings}, nil
}

func (s *Service) EnsureReleasable(ctx context.Context, m model.Manifest) error {
	if !m.Checkout.Present() || m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" {
		return nil
	}
	if err := validateCheckoutKind(m.Checkout); err != nil {
		return err
	}
	path := m.Checkout.Claim.Path
	if err := s.validateManagedCheckoutPath(path); err != nil {
		return err
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

// CommitChanges commits the checkout's uncommitted changes to the item's
// branch, so releasing the checkout keeps them. It returns the new commit, or
// "" when there was nothing to commit.
func (s *Service) CommitChanges(ctx context.Context, m model.Manifest, message string) (string, error) {
	if !m.Checkout.Present() || m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" {
		return "", nil
	}
	if err := validateCheckoutKind(m.Checkout); err != nil {
		return "", err
	}
	path := m.Checkout.Claim.Path
	if err := s.validateManagedCheckoutPath(path); err != nil {
		return "", err
	}
	if err := s.ensureCheckoutBranch(ctx, m); err != nil {
		return "", err
	}
	status, err := s.git.StatusPorcelain(ctx, path)
	if err != nil {
		return "", fmt.Errorf("could not inspect checkout status: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		return "", nil
	}
	commit, err := s.git.CommitAll(ctx, path, message)
	if err != nil {
		return "", fmt.Errorf("could not commit uncommitted changes: %w", err)
	}
	return commit, nil
}

func validateCheckoutKind(checkout model.Checkout) error {
	if checkout.Specification.Kind != model.WorkspaceKindManagedSlot {
		return fmt.Errorf("unsupported checkout kind %q", checkout.Specification.Kind)
	}
	return nil
}

func (s *Service) ReleaseCheckout(ctx context.Context, m model.Manifest) (CheckoutRelease, error) {
	return s.releaseCheckout(ctx, m)
}
