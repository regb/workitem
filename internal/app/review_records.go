package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	reviewcore "github.com/regb/workitem/internal/app/core/review"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/workspacefiles"
)

type ReviewRecordStore interface {
	LoadReview(context.Context, string) (reviewcore.State, error)
	StoreReview(context.Context, reviewcore.State, uint64, string, string, map[string]string) error
}
type ReviewMutationOptions struct {
	ResolveOptions
	Mutation        reviewcore.Mutation
	ExpectedVersion uint64
	Capture         bool
	ClaimID         string
	ClaimGeneration uint64
}
type ReviewMutationResult struct {
	State    reviewcore.State
	Receipt  reviewcore.Receipt
	Captured *reviewcore.Revision
	Reused   bool
}

func (a *App) ReviewRecordsFor(ctx context.Context, opts ResolveOptions) (reviewcore.State, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return reviewcore.State{}, err
	}
	return a.ReviewRecords.LoadReview(ctx, m.ID)
}
func reviewDigest(value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func (a *App) MutateReview(ctx context.Context, opts ReviewMutationOptions) (ReviewMutationResult, error) {
	m, guard, err := a.lockItemOperation(ctx, opts.ResolveOptions)
	if err != nil {
		return ReviewMutationResult{}, err
	}
	defer func() {
		if guard != nil {
			guard.Release()
		}
	}()
	state, err := a.ReviewRecords.LoadReview(ctx, m.ID)
	if err != nil {
		return ReviewMutationResult{}, err
	}
	r := opts.Mutation
	if !validReviewOperation(r.OperationID) {
		return ReviewMutationResult{}, errors.New("bounded review operation identity required")
	}
	digest := reviewDigest(struct {
		Mutation   reviewcore.Mutation
		Capture    bool
		ClaimID    string
		Generation uint64
	}{r, opts.Capture, opts.ClaimID, opts.ClaimGeneration})
	result := ReviewMutationResult{State: state}
	if old, ok := state.Operations[r.OperationID]; ok {
		if old.Digest != digest {
			return result, errors.New("review operation identity reused with different input")
		}
		result.Receipt = old
		result.Reused = true
		if opts.Capture {
			revision, err := state.Find(old.Revision)
			if err != nil {
				return result, err
			}
			result.Captured = &revision
		}
		return result, nil
	}
	if opts.ExpectedVersion != 0 && opts.ExpectedVersion != state.Version {
		return result, ErrReviewChanged
	}
	if r.Kind == "save_draft" {
		r.Draft.Anchor, err = a.validateReviewAnchor(ctx, m, state, r.Draft.Anchor)
		if err != nil {
			return result, err
		}
	}
	if r.Kind == "publish" {
		if _, err := a.validateReviewAnchor(ctx, m, state, reviewcore.Anchor{Revision: r.Revision, FromRevision: r.FromRevision}); err != nil {
			return result, err
		}
	}
	if opts.Capture {
		if r.Kind != "reply" || r.Revision != "" {
			return result, errors.New("capture is only valid on replies without a revision")
		}
		if err := state.ValidateReplies(r.Replies); err != nil {
			return result, err
		}
		guard.Release()
		guard = nil
		releaseCapture, err := a.lockReviewCapture(ctx, m.ID)
		if err != nil {
			return result, err
		}
		defer releaseCapture()
		state, err = a.ReviewRecords.LoadReview(ctx, m.ID)
		if err != nil {
			return result, err
		}
		if old, ok := state.Operations[r.OperationID]; ok {
			if old.Digest != digest {
				return result, errors.New("review operation identity reused with different input")
			}
			revision, err := state.Find(old.Revision)
			return ReviewMutationResult{State: state, Receipt: old, Captured: &revision, Reused: true}, err
		}
		snapshot, err := a.reviewSnapshot(ctx, m, r.OperationID, "reply", opts.ClaimID, opts.ClaimGeneration)
		if err != nil {
			return result, err
		}
		m, guard, err = a.lockItemOperation(ctx, opts.ResolveOptions)
		if err != nil {
			return result, err
		}
		state, err = a.ReviewRecords.LoadReview(ctx, m.ID)
		if err != nil {
			return result, err
		}
		if old, ok := state.Operations[r.OperationID]; ok {
			if old.Digest != digest {
				return result, errors.New("review operation identity reused with different input")
			}
			revision, err := state.Find(old.Revision)
			return ReviewMutationResult{State: state, Receipt: old, Captured: &revision, Reused: true}, err
		}
		if !reviewClaimMatches(m, opts.ClaimID, opts.ClaimGeneration) || opts.ExpectedVersion != 0 && opts.ExpectedVersion != state.Version {
			return result, ErrReviewChanged
		}
		if err := state.ValidateReplies(r.Replies); err != nil {
			return result, err
		}
		revision := state.AddRevision(snapshot)
		result.Captured = &revision
		r.Revision = revision.ID
	}
	receipt, err := state.Apply(r, time.Now().UTC())
	if err != nil {
		return result, err
	}
	receipt.Digest = digest
	if opts.Capture {
		receipt.Revision = result.Captured.ID
	}
	state.RememberOperation(r.OperationID, receipt)
	err = a.ReviewRecords.StoreReview(ctx, state, state.Version, r.OperationID, r.Kind, map[string]string{"revision_id": receipt.Revision, "round_id": receipt.Round, "draft_id": receipt.Draft, "approval_id": receipt.Approval})
	if err != nil {
		return result, err
	}
	state.Version++
	result.State = state
	result.Receipt = receipt
	return result, nil
}
func validReviewOperation(id string) bool {
	if len(id) == 0 || len(id) > 100 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (a *App) lockReviewCapture(ctx context.Context, item string) (func(), error) {
	capture, err := lock.AcquireOperation(ctx, a.Store.LockPath(item)+".review-capture")
	if err != nil {
		return nil, err
	}
	if a.ReviewCaptures != nil {
		a.ReviewCaptures.observe(item, true)
	}
	return func() {
		if a.ReviewCaptures != nil {
			a.ReviewCaptures.observe(item, false)
		}
		capture.Release()
	}, nil
}
func (a *App) reviewSnapshot(ctx context.Context, m model.Manifest, operation, trigger, claimID string, generation uint64) (reviewcore.Revision, error) {
	current, err := a.ResolveItem(ctx, ResolveOptions{Selector: m.ID})
	if err != nil {
		return reviewcore.Revision{}, err
	}
	if !reviewClaimMatches(current, claimID, generation) {
		return reviewcore.Revision{}, ErrReviewChanged
	}
	m = current
	claim := m.Checkout.Claim
	if m.State != model.StateActive || claim == nil || claim.ID != claimID || claim.Generation != generation {
		return reviewcore.Revision{}, ErrReviewChanged
	}
	g := gitpkg.New("git")
	path := claim.Path
	if err := g.ValidateReviewCheckout(ctx, m.Repository.OperationalRoot(), path); err != nil {
		return reviewcore.Revision{}, err
	}
	head, err := g.ReviewResolve(ctx, path, "HEAD")
	if err != nil {
		return reviewcore.Revision{}, err
	}
	branch, err := g.CurrentBranch(ctx, path)
	if err != nil {
		return reviewcore.Revision{}, err
	}
	snapshot, err := g.CaptureReviewSnapshot(ctx, path, head, branch)
	if err != nil {
		return reviewcore.Revision{}, err
	}
	if err := g.ValidateReviewCheckout(ctx, m.Repository.OperationalRoot(), path); err != nil {
		return reviewcore.Revision{}, err
	}
	id, err := model.NewID()
	if err != nil {
		return reviewcore.Revision{}, err
	}
	ref := "refs/wi/review/" + m.ID + "/snapshots/" + strings.ToLower(id)
	if err := g.PublishReviewSnapshot(ctx, m.Repository.OperationalRoot(), ref, snapshot.Commit); err != nil {
		return reviewcore.Revision{}, err
	}
	r := reviewcore.Revision{CaptureID: operation, Ref: ref, Commit: snapshot.Commit, Tree: snapshot.Tree, Head: snapshot.Head, Branch: snapshot.Branch, Detached: snapshot.Detached, Trigger: trigger, CreatedAt: time.Now().UTC()}
	for _, e := range snapshot.Excluded {
		r.Excluded = append(r.Excluded, reviewcore.Exclusion{Path: e.Path, Reason: e.Reason})
	}
	return r, nil
}
func (a *App) validateReviewAnchor(ctx context.Context, m model.Manifest, state reviewcore.State, anchor reviewcore.Anchor) (reviewcore.Anchor, error) {
	revision, err := state.Find(anchor.Revision)
	if err != nil {
		return anchor, err
	}
	base := revision.Base
	if anchor.FromRevision != "" {
		from, err := state.Find(anchor.FromRevision)
		if err != nil {
			return anchor, err
		}
		base = from.Commit
	}
	if anchor.Path == "" {
		if anchor.Side != "" || anchor.StartLine != 0 || anchor.EndLine != 0 {
			return anchor, errors.New("item-level anchor cannot have a line")
		}
		return anchor, nil
	}
	if err := workspacefiles.ValidPath(anchor.Path); err != nil {
		return anchor, err
	}
	endpoint := revision.Commit
	if anchor.Side == "old" {
		endpoint = base
	} else if anchor.Side != "new" {
		return anchor, errors.New("anchor side must be old or new")
	}
	file, err := gitpkg.New("git").ReviewSource(ctx, m.Repository.OperationalRoot(), endpoint, anchor.Path)
	if err != nil {
		return anchor, err
	}
	if !file.Exists || file.SHA256 == "" {
		return anchor, errors.New("immutable file cannot be anchored")
	}
	if anchor.BlobSHA256 != "" && anchor.BlobSHA256 != file.SHA256 {
		return anchor, ErrReviewChanged
	}
	anchor.BlobSHA256 = file.SHA256
	if anchor.StartLine > 0 {
		if file.Binary || file.TooLarge {
			return anchor, errors.New("line anchor requires text")
		}
		if anchor.EndLine == 0 {
			anchor.EndLine = anchor.StartLine
		}
		lines := strings.Count(file.Content, "\n")
		if file.Content != "" && !strings.HasSuffix(file.Content, "\n") {
			lines++
		}
		if anchor.EndLine < anchor.StartLine || int(anchor.EndLine) > lines {
			return anchor, errors.New("anchor range outside immutable file")
		}
	} else if anchor.EndLine != 0 {
		return anchor, errors.New("invalid line range")
	}
	return anchor, nil
}
func (a *App) MarkReviewNotified(ctx context.Context, opts ResolveOptions, round, command string) error {
	m, guard, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return err
	}
	defer guard.Release()
	state, err := a.ReviewRecords.LoadReview(ctx, m.ID)
	if err != nil {
		return err
	}
	q, err := state.Round(round)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	q.NotifiedAt = &now
	q.CommandID = command
	operation, err := model.NewID()
	if err != nil {
		return err
	}
	return a.ReviewRecords.StoreReview(ctx, state, state.Version, operation, "notified", map[string]string{"round_id": round, "command_id": command})
}
func (a *App) DeleteReviewSnapshots(ctx context.Context, m model.Manifest) error {
	return gitpkg.New("git").DeleteItemReviewRefs(ctx, m.Repository.OperationalRoot(), m.ID)
}
func (a *App) ReviewThread(ctx context.Context, opts ResolveOptions, id string) (reviewcore.Thread, error) {
	state, err := a.ReviewRecordsFor(ctx, opts)
	if err != nil {
		return reviewcore.Thread{}, err
	}
	t, err := state.Thread(id)
	if err != nil {
		return reviewcore.Thread{}, fmt.Errorf("%w: %s", err, id)
	}
	return *t, nil
}
