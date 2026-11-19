package daemonapi

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func (s *Server) notifyReviewRound(ctx context.Context, item, round, messageID string) (*v1.ReviewRound, error) {
	who := caller(ctx)
	if who.client == "" {
		who.client = "review-notifier"
		who.holder = model.LeaseHolderCLI
	}
	ctx = context.WithValue(ctx, identityKey{}, who)
	q, err := s.App.NotifyReviewRound(ctx, app.ReviewNotificationOptions{ResolveOptions: resolve(item), RoundID: round, MessageID: messageID, ClientID: who.client, Holder: who.holder}, func(ctx context.Context, n app.ReviewNotification) (string, error) {
		delivery := v1.Delivery_DELIVERY_PROMPT
		if n.FollowUp {
			delivery = v1.Delivery_DELIVERY_FOLLOW_UP
		}
		err := s.send(ctx, &v1.SendMessageRequest{ItemId: n.ItemID, Text: n.Text, ClientMessageId: n.MessageID, ExpectedGeneration: n.Generation, Delivery: delivery, LeaseId: n.LeaseID})
		return messageCommandID(n.ItemID, n.MessageID), err
	})
	if errors.Is(err, app.ErrReviewNotificationUnavailable) {
		return nil, precondition(err.Error())
	}
	if err != nil {
		return nil, err
	}
	return reviewRound(q), nil
}
func (s *Server) NotifyReviewRound(ctx context.Context, req *connect.Request[v1.NotifyReviewRoundRequest]) (*connect.Response[v1.NotifyReviewRoundResponse], error) {
	r := req.Msg
	id := r.ClientMessageId
	if id == "" {
		var err error
		id, err = model.NewID()
		if err != nil {
			return nil, err
		}
	}
	round, err := s.notifyReviewRound(ctx, r.ItemId, r.RoundId, id)
	if err != nil {
		return nil, partial(err, r.ItemId, "review round remains published; notification failed")
	}
	return response(&v1.NotifyReviewRoundResponse{Round: round}, nil)
}
