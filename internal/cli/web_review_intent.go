package cli

import (
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	webaccess "github.com/regb/workitem/internal/web"
)

func (b webBackend) ReviewIntent(ctx context.Context, id, revision string) (*v1.GetReviewIntentResponse, error) {
	v, err := b.cfg.API.Reviews.GetReviewIntent(ctx, webRequest(ctx, &v1.GetReviewIntentRequest{ItemId: id, RevisionId: revision}))
	if err != nil {
		return nil, err
	}
	return v.Msg, nil
}
func (b webBackend) SetReviewIntent(ctx context.Context, id string, r *v1.SetReviewIntentRequest) webaccess.Outcome {
	r.ItemId = id
	v, err := b.cfg.API.Reviews.SetReviewIntent(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(v.Msg.Result, nil, nil)
}
func (b webBackend) NotifyReviewRound(ctx context.Context, id string, r *v1.NotifyReviewRoundRequest) webaccess.Outcome {
	r.ItemId = id
	v, err := b.cfg.API.Reviews.NotifyReviewRound(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(v.Msg.Round, nil, nil)
}
