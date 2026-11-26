package cli

import (
	"context"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// The lists that grow with an item's life come in pages. A command that
// shows or searches the whole of one reads every page into one response.

// maxPages bounds the pages a command reads, against a list that keeps
// growing as it is read.
const maxPages = 1000

func allNotes(ctx context.Context, cfg Config, itemID string) (*v1.ListNotesResponse, error) {
	var out *v1.ListNotesResponse
	token := ""
	for range maxPages {
		value, err := cfg.API.Items.ListNotes(ctx, rpcRequest(cfg, &v1.ListNotesRequest{ItemId: itemID, PageToken: token}))
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = value.Msg
		} else {
			out.Notes = append(out.Notes, value.Msg.Notes...)
		}
		if token = value.Msg.NextPageToken; token == "" {
			break
		}
	}
	out.NextPageToken = ""
	return out, nil
}

func allThreads(ctx context.Context, cfg Config, itemID string) (*v1.GetReviewThreadsResponse, error) {
	var out *v1.GetReviewThreadsResponse
	token := ""
	for range maxPages {
		value, err := cfg.API.Reviews.GetReviewThreads(ctx, rpcRequest(cfg, &v1.GetReviewThreadsRequest{ItemId: itemID, PageToken: token}))
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = value.Msg
		} else {
			out.Threads = append(out.Threads, value.Msg.Threads...)
		}
		if token = value.Msg.NextPageToken; token == "" {
			break
		}
	}
	out.NextPageToken = ""
	return out, nil
}

func allRevisions(ctx context.Context, cfg Config, itemID string) (*v1.ListReviewRevisionsResponse, error) {
	var out *v1.ListReviewRevisionsResponse
	token := ""
	for range maxPages {
		value, err := cfg.API.Reviews.ListReviewRevisions(ctx, rpcRequest(cfg, &v1.ListReviewRevisionsRequest{ItemId: itemID, PageToken: token}))
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = value.Msg
		} else {
			out.Revisions = append(out.Revisions, value.Msg.Revisions...)
		}
		if token = value.Msg.NextPageToken; token == "" {
			break
		}
	}
	out.NextPageToken = ""
	return out, nil
}
