package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/regb/workitem/internal/apperr"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/workspacefiles"
)

var ErrReviewChanged = apperr.Stalef("review revision or workspace claim changed since it was opened; refresh before continuing")

type ReviewOptions struct{ Comparison, Commit, BaseCommit string }
type ReviewRevision struct {
	Version         string `json:"version"`
	BaseCommit      string `json:"base_commit"`
	HeadCommit      string `json:"head_commit"`
	ClaimID         string `json:"claim_id"`
	ClaimGeneration uint32 `json:"claim_generation"`
	WorkingTree     bool   `json:"working_tree"`
}
type Review struct {
	Revision          ReviewRevision      `json:"revision"`
	Files             []gitpkg.ReviewPath `json:"files"`
	Truncated         bool                `json:"truncated"`
	Branch            string              `json:"branch"`
	IntegrationBranch string              `json:"integration_branch"`
}

type reviewGit interface {
	ReviewResolve(context.Context, string, string) (string, error)
	ReviewMergeBase(context.Context, string, string, string) (string, error)
	ReviewParent(context.Context, string, string) (string, error)
	ReviewFiles(context.Context, string, string, string, bool) ([]gitpkg.ReviewPath, bool, error)
	ReviewSource(context.Context, string, string, string) (workspacefiles.File, error)
	ReviewCommits(context.Context, string, string, string) ([]gitpkg.ReviewCommit, bool, error)
	ReviewContains(context.Context, string, string, string) (bool, error)
	ReviewBranches(context.Context, string, string, string) ([]gitpkg.ReviewBranch, []string, error)
	ReviewGraph(context.Context, string, string, string) (gitpkg.ReviewGraph, error)
}

func (a *App) reviewGit() (reviewGit, error) {
	g, ok := a.Git.(reviewGit)
	if !ok {
		return nil, errors.New("Git adapter does not support review")
	}
	return g, nil
}

// reviewRevision resolves only Wi-owned branch choices. Arbitrary refs, working
// directories, branch switching and upstream refresh are deliberately absent.
func (a *App) reviewRevision(ctx context.Context, m model.Manifest, g reviewGit, opts ReviewOptions) (ReviewRevision, string, error) {
	dir := m.Repository.OperationalRoot()
	head, err := g.ReviewResolve(ctx, dir, "refs/heads/"+expectedCheckoutBranch(m))
	if err != nil {
		return ReviewRevision{}, "", err
	}
	base := m.Repository.CreatedFromCommit
	working := false
	switch opts.Comparison {
	case "", "item":
		working = m.Checkout.Claim != nil
	case "committed":
	case "uncommitted":
		base = head
		working = true
	case "integration":
		integration, err := g.ReviewResolve(ctx, dir, "refs/heads/"+m.Repository.IntegrationBranch)
		if err != nil {
			return ReviewRevision{}, "", err
		}
		base, err = g.ReviewMergeBase(ctx, dir, head, integration)
		if err != nil {
			return ReviewRevision{}, "", err
		}
	case "commit":
		head, err = resolveReviewCommit(ctx, m, g, opts.Commit)
		if err != nil {
			return ReviewRevision{}, "", err
		}
		base, err = g.ReviewParent(ctx, dir, head)
		if err != nil {
			return ReviewRevision{}, "", err
		}
	default:
		return ReviewRevision{}, "", errors.New("invalid review comparison")
	}
	if opts.BaseCommit != "" {
		if opts.BaseCommit == m.Repository.CreatedFromCommit {
			base, err = g.ReviewResolve(ctx, dir, opts.BaseCommit)
		} else {
			base, err = resolveReviewCommit(ctx, m, g, opts.BaseCommit)
		}
		if err != nil {
			return ReviewRevision{}, "", err
		}
	}
	r := ReviewRevision{BaseCommit: base, HeadCommit: head, WorkingTree: working}
	if working {
		if m.Checkout.Claim == nil {
			return r, "", errors.New("item has no workspace claim")
		}
		claim := m.Checkout.Claim
		r.ClaimID = claim.ID
		r.ClaimGeneration = uint32(claim.Generation)
		dir = claim.Path
		branch, err := a.Git.CurrentBranch(ctx, dir)
		if err != nil {
			return r, "", err
		}
		if branch != expectedCheckoutBranch(m) {
			return r, "", errors.New("checkout branch does not match its claim")
		}
		actual, err := g.ReviewResolve(ctx, dir, "HEAD")
		if err != nil {
			return r, "", err
		}
		if actual != head {
			return r, "", ErrReviewChanged
		}
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%t", m.ID, base, head, r.ClaimID, r.ClaimGeneration, working)))
	r.Version = hex.EncodeToString(sum[:])
	return r, dir, nil
}

func resolveReviewCommit(ctx context.Context, m model.Manifest, g reviewGit, sha string) (string, error) {
	if len(sha) != 40 && len(sha) != 64 {
		return "", errors.New("invalid commit SHA")
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return "", errors.New("invalid commit SHA")
	}
	dir := m.Repository.OperationalRoot()
	selected, err := g.ReviewResolve(ctx, dir, sha)
	if err != nil {
		return "", err
	}
	branches, _, err := g.ReviewBranches(ctx, dir, expectedCheckoutBranch(m), m.Repository.IntegrationBranch)
	if err != nil {
		return "", err
	}
	for _, branch := range branches {
		if branch.SHA == "" {
			continue
		}
		contains, err := g.ReviewContains(ctx, dir, selected, branch.SHA)
		if err != nil {
			return "", err
		}
		if contains {
			return selected, nil
		}
	}
	return "", errors.New("commit is not reachable from the item's history branches")
}

func (a *App) GetItemReview(ctx context.Context, opts ResolveOptions, comparison ReviewOptions) (Review, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return Review{}, err
	}
	defer operation.Release()
	g, err := a.reviewGit()
	if err != nil {
		return Review{}, err
	}
	r, dir, err := a.reviewRevision(ctx, m, g, comparison)
	if err != nil {
		return Review{}, err
	}
	files, truncated, err := g.ReviewFiles(ctx, dir, r.BaseCommit, r.HeadCommit, r.WorkingTree)
	return Review{r, files, truncated, expectedCheckoutBranch(m), m.Repository.IntegrationBranch}, err
}

func (a *App) GetItemReviewFile(ctx context.Context, opts ResolveOptions, comparison ReviewOptions, p, expected string) (ReviewRevision, workspacefiles.File, workspacefiles.File, error) {
	empty := workspacefiles.File{}
	if err := workspacefiles.ValidPath(p); err != nil {
		return ReviewRevision{}, empty, empty, err
	}
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return ReviewRevision{}, empty, empty, err
	}
	defer operation.Release()
	g, err := a.reviewGit()
	if err != nil {
		return ReviewRevision{}, empty, empty, err
	}
	r, dir, err := a.reviewRevision(ctx, m, g, comparison)
	if err != nil {
		return r, empty, empty, err
	}
	if expected == "" || r.Version != expected {
		return r, empty, empty, ErrReviewChanged
	}
	old, err := g.ReviewSource(ctx, dir, r.BaseCommit, p)
	if err != nil {
		return r, empty, empty, err
	}
	var next workspacefiles.File
	if r.WorkingTree {
		next, err = workspacefiles.Read(dir, p)
	} else {
		next, err = g.ReviewSource(ctx, dir, r.HeadCommit, p)
	}
	return r, old, next, err
}
func (a *App) ItemReviewCommits(ctx context.Context, opts ResolveOptions) ([]gitpkg.ReviewCommit, bool, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return nil, false, err
	}
	defer operation.Release()
	g, err := a.reviewGit()
	if err != nil {
		return nil, false, err
	}
	r, dir, err := a.reviewRevision(ctx, m, g, ReviewOptions{Comparison: "committed"})
	if err != nil {
		return nil, false, err
	}
	return g.ReviewCommits(ctx, dir, r.BaseCommit, r.HeadCommit)
}

func (a *App) ItemReviewGraph(ctx context.Context, opts ResolveOptions) (gitpkg.ReviewGraph, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return gitpkg.ReviewGraph{}, err
	}
	defer operation.Release()
	g, err := a.reviewGit()
	if err != nil {
		return gitpkg.ReviewGraph{}, err
	}
	return g.ReviewGraph(ctx, m.Repository.OperationalRoot(), expectedCheckoutBranch(m), m.Repository.IntegrationBranch)
}
