package app

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/regb/workitem/internal/executor"
	"github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

func (a *App) executorClient(m model.Manifest) (executor.Client, error) {
	claim := m.Checkout.Claim
	if claim == nil || claim.Executor == nil {
		return executor.Client{}, fmt.Errorf("item has no executor claim")
	}
	b := claim.Executor
	return executor.Client{Profile: executor.Profile{Driver: b.Driver, Image: b.Image, Network: b.Network, Host: b.Host, Root: b.Root, SSHConfig: b.SSHConfig}, Identity: executor.Identity{Name: b.Name, Owner: a.MasterID + ":" + m.ID, Claim: claim.ID, Generation: claim.Generation}, Transport: a.ExecutorTransport}, nil
}

func (a *App) ensureExecutorWorkspace(ctx context.Context, m model.Manifest) (model.Manifest, []string, error) {
	guard, err := lock.Acquire(ctx, a.Store.LockPath(m.ID)+".executor")
	if err != nil {
		return m, nil, err
	}
	defer guard.Release()
	m, err = a.Store.LoadManifest(m.ID)
	if err != nil {
		return m, nil, err
	}
	if m.State == model.StateArchived {
		return m, nil, fmt.Errorf("archived item cannot materialize an executor workspace")
	}
	if m.Checkout.Claim == nil {
		profile, ok := a.ExecutorsConfig[m.Checkout.Specification.ExecutorProfile]
		if !ok {
			return m, nil, fmt.Errorf("executor profile %q is not configured", m.Checkout.Specification.ExecutorProfile)
		}
		if err := profile.Validate(); err != nil {
			return m, nil, err
		}
		claim, err := m.Checkout.AssignNewClaim(m.Checkout.Specification.ExecutorProfile, model.RepositoryKey(m.Repository), "", executor.Workdir)
		if err != nil {
			return m, nil, err
		}
		claim.Executor = &model.ExecutorClaim{Name: executor.Name(a.MasterID, m.ID, claim.Generation), Driver: profile.Driver, Image: profile.Image, Network: profile.Network, Host: profile.Host, Root: profile.Root, SSHConfig: profile.SSHConfig}
		client, err := a.executorClient(m)
		if err != nil {
			return m, nil, err
		}
		claim.Path = client.Workdir()
		m.UpdatedAt = a.now()
		if err := a.Store.SaveManifest(ctx, m); err != nil {
			return m, nil, err
		}
		_ = a.Store.AppendEvent(ctx, m.ID, model.NewEvent(a.now(), "checkout.claimed", "wi", map[string]any{"claim_id": claim.ID, "generation": claim.Generation, "executor": claim.Executor.Name}))
	}
	c, err := a.executorClient(m)
	if err != nil {
		return m, nil, err
	}
	if m.Checkout.Claim.Executor.Releasing {
		return m, nil, fmt.Errorf("executor release is incomplete; retry workspace release before starting")
	}
	if m.Checkout.Claim.Executor.Ready {
		if err := c.Start(ctx); err != nil {
			return m, nil, fmt.Errorf("existing executor is unavailable; claim retained: %w", err)
		}
		return m, nil, nil
	}
	if err := c.Ensure(ctx); err != nil {
		return m, nil, fmt.Errorf("executor claim retained; retry workspace ensure: %w", err)
	}
	branch := m.Checkout.Specification.Branch
	commit := m.Repository.CreatedFromCommit
	if head, err := executor.Git(ctx, m.Repository.OperationalRoot(), "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}"); err == nil {
		commit = head
	}
	// A previous released generation's export is the implementation history.
	if m.Checkout.ClaimGeneration > 1 {
		for generation := m.Checkout.ClaimGeneration - 1; generation > 0; generation-- {
			if head, err := executor.Git(ctx, m.Repository.OperationalRoot(), "rev-parse", "--verify", executor.QuarantineRef(m.ID, generation)+"^{commit}"); err == nil {
				commit = head
				break
			}
		}
	}
	base, _ := a.Git.DefaultBranch(ctx, m.Repository.OperationalRoot())
	bundle, cleanup, err := executor.Bundle(ctx, m.Repository.OperationalRoot(), branch, commit, base)
	if err != nil {
		return m, nil, err
	}
	defer cleanup()
	file, err := os.Open(bundle)
	if err != nil {
		return m, nil, err
	}
	defer file.Close()
	name, _ := executor.Git(ctx, m.Repository.OperationalRoot(), "config", "user.name")
	email, _ := executor.Git(ctx, m.Repository.OperationalRoot(), "config", "user.email")
	if name == "" {
		name = "Wi"
	}
	if email == "" {
		email = "wi@localhost"
	}
	if err := c.Seed(ctx, file, branch, name, email); err != nil {
		return m, nil, err
	}
	m.Checkout.Claim.Executor.Ready = true
	m.UpdatedAt = a.now()
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		return m, nil, err
	}
	_ = a.Store.AppendEvent(ctx, m.ID, model.NewEvent(a.now(), "checkout.assigned", "wi", map[string]any{"claim_id": m.Checkout.Claim.ID, "generation": m.Checkout.Claim.Generation, "executor": m.Checkout.Claim.Executor.Name}))
	return m, nil, nil
}

func (a *App) exportExecutor(ctx context.Context, m model.Manifest) (model.Manifest, error) {
	c, err := a.executorClient(m)
	if err != nil {
		return m, err
	}
	head, err := c.Import(ctx, m.Repository.OperationalRoot(), m.ID, m.Checkout.Specification.Branch)
	if err != nil {
		return m, err
	}
	m.Checkout.Claim.Executor.ExportedHead = head
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		return m, err
	}
	return m, nil
}

func (a *App) releaseExecutor(ctx context.Context, m model.Manifest, force bool) (model.Manifest, error) {
	guard, err := lock.Acquire(ctx, a.Store.LockPath(m.ID)+".executor")
	if err != nil {
		return m, err
	}
	defer guard.Release()
	m, err = a.Store.LoadManifest(m.ID)
	if err != nil {
		return m, err
	}
	if m.Checkout.Claim == nil {
		return m, nil
	}
	c, err := a.executorClient(m)
	if err != nil {
		return m, err
	}
	b := m.Checkout.Claim.Executor
	exists, err := c.Exists(ctx)
	if err != nil {
		return m, err
	}
	if !exists && b.Ready && !b.Destroying && !force {
		return m, fmt.Errorf("executor is missing; use --force to release its claim using only previously exported work")
	}
	if exists && b.Ready && !b.Destroying {
		if !b.Releasing {
			status, err := c.Status(ctx)
			if err != nil {
				return m, err
			}
			if status.Branch != m.Checkout.Specification.Branch {
				return m, fmt.Errorf("executor branch differs from item branch")
			}
			if status.Dirty && !force {
				return m, fmt.Errorf("executor has uncommitted changes; commit them or explicitly force release")
			}
			b.Releasing = true
			if err := a.Store.SaveManifest(ctx, m); err != nil {
				return m, err
			}
		}
		if err := c.Release(ctx); err != nil {
			return m, err
		}
		status, err := c.Status(ctx)
		if err != nil {
			return m, err
		}
		if status.Dirty && !force {
			if err := c.Resume(ctx); err != nil {
				return m, err
			}
			b.Releasing = false
			if err := a.Store.SaveManifest(ctx, m); err != nil {
				return m, err
			}
			return m, fmt.Errorf("executor changed during release; retained for repair in its shell")
		}
		m, err = a.exportExecutor(ctx, m)
		if err != nil {
			return m, err
		}
	}
	b.Destroying = true
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		return m, err
	}
	if err := c.Destroy(ctx); err != nil {
		return m, err
	}
	claim := m.Checkout.Claim
	m.Checkout.ClearClaim()
	m.UpdatedAt = a.now()
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		return m, err
	}
	_ = a.Store.AppendEvent(ctx, m.ID, model.NewEvent(a.now(), "workspace.released", "user", map[string]any{"claim_id": claim.ID, "generation": claim.Generation, "forced": force, "exported_head": claim.Executor.ExportedHead}))
	return m, nil
}

func (a *App) AttachExecutor(ctx context.Context, opts ResolveOptions, in io.Reader, out, stderr io.Writer) error {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return err
	}
	c, err := a.executorClient(m)
	if err != nil {
		return err
	}
	return c.Attach(ctx, in, out, stderr)
}

func executorEnvironmentNote(m model.Manifest) string {
	b := m.Checkout.Claim.Executor
	network := b.Network
	if network == "" {
		network = "none"
	}
	environment := fmt.Sprintf("Docker executor %s, network %s", b.Name, network)
	if b.Driver == "ssh" {
		environment = fmt.Sprintf("SSH host %s, owned workspace %s", b.Host, b.Name)
	}
	return fmt.Sprintf("\n\n## Wi executor\nWorkspace: %s in %s, profile %s. Tools and ! commands run in the executor; Pi and its conversation stay on the host. Commit work on %s. origin is a host-provided Git bundle: fetch works, push does not. The host runs wi merge. Wi supplies no forwarded credentials or executable. SSH commands have the remote account's permissions. Use noninteractive commands. Editors and development tools run in the executor shell.\n", m.Checkout.Claim.Path, environment, m.Checkout.Specification.ExecutorProfile, m.Checkout.Specification.Branch)
}
