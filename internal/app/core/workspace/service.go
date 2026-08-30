package workspace

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type Store interface {
	SaveManifest(context.Context, model.Manifest) error
	ClaimRepositoryHome(context.Context, model.Manifest) error
	AppendEvent(context.Context, string, model.Event) error
	LoadAgentRuntime(string) (*model.AgentRuntime, error)
	ListManifests() ([]model.Manifest, []error)
	WorktreesDir() string
}

type Git interface {
	DetectRepository(context.Context, string, string) (model.RepositoryInfo, error)
	Head(context.Context, string) (string, error)
	StatusPorcelain(context.Context, string) (string, error)
	CurrentBranch(context.Context, string) (string, error)
	BranchExists(context.Context, string, string) (bool, error)
	WorktreeAdd(context.Context, model.WorktreeAddOptions) error
	Switch(context.Context, string, string, string, bool) error
	WorktreeRemove(context.Context, string, string, bool) error
}

type Tmux interface {
	HasSession(context.Context, string) (bool, error)
}

type Direnv interface {
	Status(context.Context, string) (model.DirenvStatus, error)
	Deny(context.Context, string) error
}

type Resolver func(context.Context, contract.ResolveOptions) (model.Manifest, error)
type OwnershipObserver func(*model.AgentRuntime) bool

type Service struct {
	store     Store
	git       Git
	tmux      Tmux
	direnv    Direnv
	resolve   Resolver
	ownership OwnershipObserver
	now       func() time.Time
}

func New(st Store, git Git, tmux Tmux, direnv Direnv, resolve Resolver, ownership OwnershipObserver, now func() time.Time) *Service {
	return &Service{store: st, git: git, tmux: tmux, direnv: direnv, resolve: resolve, ownership: ownership, now: now}
}

type GitStatus struct {
	CurrentHead     string `json:"current_head"`
	ExpectedBranch  string `json:"expected_branch,omitempty"`
	CurrentBranch   string `json:"current_branch,omitempty"`
	BranchMatches   bool   `json:"branch_matches"`
	BranchMismatch  bool   `json:"branch_mismatch,omitempty"`
	Dirty           bool   `json:"dirty"`
	StatusPorcelain string `json:"status_porcelain"`
}
type StatusResult struct {
	WorkItemID string         `json:"work_item_id"`
	State      string         `json:"state"`
	Checkout   model.Checkout `json:"checkout"`
	Git        GitStatus      `json:"git"`
	Manifest   model.Manifest `json:"manifest"`
	Warnings   []string       `json:"warnings"`
}

type RelocateRepositoryResult struct {
	WorkItemID   string         `json:"work_item_id"`
	PreviousRoot string         `json:"previous_root"`
	CurrentRoot  string         `json:"current_root"`
	Changed      bool           `json:"changed"`
	Manifest     model.Manifest `json:"manifest"`
}

func ExpectedBranch(m model.Manifest) string {
	return strings.TrimSpace(m.Checkout.Branch)
}

func (s *Service) RelocateRepository(ctx context.Context, opts contract.ResolveOptions, path string) (RelocateRepositoryResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return RelocateRepositoryResult{}, err
	}
	if m.Checkout.Present() {
		return RelocateRepositoryResult{}, fmt.Errorf("cannot relocate repository while checkout %s is assigned; release the workspace first", *m.Checkout.Path)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return RelocateRepositoryResult{}, fmt.Errorf("repository path is required")
	}
	candidate, err := s.git.DetectRepository(ctx, path, m.Repository.CreatedFromCommit)
	if err != nil {
		return RelocateRepositoryResult{}, fmt.Errorf("inspect replacement repository: %w", err)
	}
	if recorded := strings.TrimSpace(m.Repository.RemoteURL); recorded != "" && candidate.Repository.RemoteURL != recorded {
		return RelocateRepositoryResult{}, fmt.Errorf("replacement repository origin %q does not match recorded origin %q", candidate.Repository.RemoteURL, recorded)
	}
	previous := m.Repository.OperationalRoot()
	current := candidate.Repository.RootAtCreation
	result := RelocateRepositoryResult{WorkItemID: m.ID, PreviousRoot: previous, CurrentRoot: current, Manifest: m}
	if current == previous {
		return result, nil
	}
	now := s.now()
	m.Repository.CurrentRoot = current
	m.UpdatedAt = now
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return RelocateRepositoryResult{}, err
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "repository.relocated", "user", map[string]any{"previous_root": previous, "current_root": current}))
	result.Changed, result.Manifest = true, m
	return result, nil
}

func (s *Service) Status(ctx context.Context, opts contract.ResolveOptions) (StatusResult, error) {
	m, e := s.resolve(ctx, opts)
	if e != nil {
		return StatusResult{}, e
	}
	res := StatusResult{WorkItemID: m.ID, State: m.State, Checkout: m.Checkout, Manifest: m, Warnings: []string{}}
	res.Git.ExpectedBranch = ExpectedBranch(m)
	if !m.Checkout.Present() || m.Checkout.Path == nil || *m.Checkout.Path == "" {
		res.Warnings = append(res.Warnings, "checkout is absent")
		return res, nil
	}
	if _, e = os.Stat(*m.Checkout.Path); e != nil {
		res.Warnings = append(res.Warnings, "checkout path is unavailable: "+e.Error())
		return res, nil
	}
	if b, e := s.git.CurrentBranch(ctx, *m.Checkout.Path); e == nil {
		res.Git.CurrentBranch = b
		res.Git.BranchMatches = b == res.Git.ExpectedBranch
		res.Git.BranchMismatch = b != "" && b != res.Git.ExpectedBranch
	} else {
		res.Warnings = append(res.Warnings, "could not inspect checkout branch: "+e.Error())
	}
	if h, e := s.git.Head(ctx, *m.Checkout.Path); e == nil {
		res.Git.CurrentHead = h
	} else {
		res.Warnings = append(res.Warnings, "could not resolve checkout HEAD: "+e.Error())
	}
	if x, e := s.git.StatusPorcelain(ctx, *m.Checkout.Path); e == nil {
		res.Git.StatusPorcelain = x
		res.Git.Dirty = strings.TrimSpace(x) != ""
	} else {
		res.Warnings = append(res.Warnings, "could not inspect checkout status: "+e.Error())
	}
	return res, nil
}
