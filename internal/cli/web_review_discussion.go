package cli

import (
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	webaccess "github.com/regb/workitem/internal/web"
)

func (b webBackend) ReviewThreads(ctx context.Context, id string) (*v1.GetReviewThreadsResponse, error) {
	value, err := b.cfg.API.Reviews.GetReviewThreads(ctx, webRequest(ctx, &v1.GetReviewThreadsRequest{ItemId: id}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func (b webBackend) ReviewDrafts(ctx context.Context, id string) (*v1.GetReviewDraftsResponse, error) {
	value, err := b.cfg.API.Reviews.GetReviewDrafts(ctx, webRequest(ctx, &v1.GetReviewDraftsRequest{ItemId: id}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func (b webBackend) SaveReviewDraft(ctx context.Context, id string, r *v1.SaveReviewDraftRequest) webaccess.Outcome {
	r.ItemId = id
	value, err := b.cfg.API.Reviews.SaveReviewDraft(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg.Result, nil, nil)
}
func (b webBackend) DeleteReviewDrafts(ctx context.Context, id string, r *v1.DeleteReviewDraftsRequest) webaccess.Outcome {
	r.ItemId = id
	value, err := b.cfg.API.Reviews.DeleteReviewDrafts(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg.Result, nil, nil)
}
func (b webBackend) PublishReviewRound(ctx context.Context, id string, r *v1.PublishReviewRoundRequest) webaccess.Outcome {
	r.ItemId = id
	value, err := b.cfg.API.Reviews.PublishReviewRound(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg.Result, nil, nil)
}
func (b webBackend) ReplyReviewThreads(ctx context.Context, id string, r *v1.ReplyReviewThreadsRequest) webaccess.Outcome {
	r.ItemId = id
	value, err := b.cfg.API.Reviews.ReplyReviewThreads(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg.Result, nil, nil)
}
