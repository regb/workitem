package app

import (
	"context"
	"errors"

	reviewcore "github.com/regb/workitem/internal/app/core/review"
	"github.com/regb/workitem/internal/model"
)

type CheckpointOptions struct {
	ResolveOptions
	CaptureID, ClaimID, Trigger string
	ClaimGeneration             uint64
}
type CheckpointResult struct {
	State    reviewcore.State
	Revision reviewcore.Revision
	Reused   bool
}

func (a *App) CaptureCheckpoint(ctx context.Context, opts CheckpointOptions) (CheckpointResult, error) {
	if !validReviewOperation(opts.CaptureID) {
		return CheckpointResult{}, errors.New("capture identity required")
	}
	m, err := a.ResolveItem(ctx, opts.ResolveOptions)
	if err != nil {
		return CheckpointResult{}, err
	}
	digest := reviewDigest(struct {
		ID, Claim, Trigger string
		Generation         uint64
	}{opts.CaptureID, opts.ClaimID, opts.Trigger, opts.ClaimGeneration})
	existing := func(state reviewcore.State) (CheckpointResult, bool, error) {
		if old, ok := state.Operations[opts.CaptureID]; ok {
			if old.Digest != digest {
				return CheckpointResult{}, true, errors.New("capture identity reused with different input")
			}
			r, err := state.Find(old.Revision)
			return CheckpointResult{state, r, true}, true, err
		}
		return CheckpointResult{}, false, nil
	}
	state, err := a.ReviewRecords.LoadReview(ctx, m.ID)
	if err != nil {
		return CheckpointResult{}, err
	}
	if out, found, err := existing(state); found {
		return out, err
	}
	releaseCapture, err := a.lockReviewCapture(ctx, m.ID)
	if err != nil {
		return CheckpointResult{}, err
	}
	defer releaseCapture()
	state, err = a.ReviewRecords.LoadReview(ctx, m.ID)
	if err != nil {
		return CheckpointResult{}, err
	}
	if out, found, err := existing(state); found {
		return out, err
	}
	trigger := opts.Trigger
	if trigger == "" {
		trigger = "manual"
	}
	snapshot, err := a.reviewSnapshot(ctx, m, opts.CaptureID, trigger, opts.ClaimID, opts.ClaimGeneration)
	if err != nil {
		return CheckpointResult{}, err
	}
	// Git work never holds the item operation lock: sends and controls remain
	// independent. Recheck ownership and allocate the revision under that lock.
	m, guard, err := a.lockItemOperation(ctx, opts.ResolveOptions)
	if err != nil {
		return CheckpointResult{}, err
	}
	defer guard.Release()
	state, err = a.ReviewRecords.LoadReview(ctx, m.ID)
	if err != nil {
		return CheckpointResult{}, err
	}
	if out, found, err := existing(state); found {
		return out, err
	}
	if !reviewClaimMatches(m, opts.ClaimID, opts.ClaimGeneration) {
		return CheckpointResult{}, ErrReviewChanged
	}
	r := state.AddRevision(snapshot)
	state.RememberOperation(opts.CaptureID, reviewcore.Receipt{Kind: "capture", Revision: r.ID, Digest: digest})
	if err := a.ReviewRecords.StoreReview(ctx, state, state.Version, opts.CaptureID, "revision_created", map[string]string{"revision_id": r.ID}); err != nil {
		return CheckpointResult{}, err
	}
	state.Version++
	return CheckpointResult{state, r, false}, nil
}
func reviewClaimMatches(m model.Manifest, id string, generation uint64) bool {
	return m.State == model.StateActive && m.Checkout.Claim != nil && m.Checkout.Claim.ID == id && m.Checkout.Claim.Generation == generation
}
