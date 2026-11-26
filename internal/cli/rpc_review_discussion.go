package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
	"io"
	"strings"
)

func rpcReviewDiscussion(ctx context.Context, command string, args []string, cfg Config, jsonOut bool) error {
	fs := flag.NewFlagSet("review "+command, flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	item := fs.String("item", "", "work item")
	revision := fs.String("revision", "", "revision")
	from := fs.String("from", "", "comparison revision")
	operation := fs.String("operation-id", "", "stable operation identity")
	message := fs.String("message", "", "comment or reply")
	fs.StringVar(message, "m", "", "comment or reply")
	path := fs.String("path", "", "file path")
	side := fs.String("side", "new", "old or new")
	line := fs.Uint("line", 0, "line number")
	end := fs.Uint("end-line", 0, "last line")
	thread := fs.String("thread", "", "thread identity")
	drafts := fs.String("drafts", "", "comma-separated exact draft identities")
	addressed := fs.Bool("addressed", false, "addressed evidence, not resolution")
	capture := fs.Bool("capture", false, "capture current code with replies")
	stdin := fs.Bool("stdin", false, "read JSON reply batch")
	unresolved := fs.Bool("unresolved", false, "only unresolved threads")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	id, err := rpcSelect(ctx, cfg, *item)
	if err != nil {
		return err
	}
	if command == "threads" || command == "thread" {
		out, err := allThreads(ctx, cfg, id)
		if err != nil {
			return err
		}
		selected := ""
		if command == "thread" {
			if fs.NArg() != 1 {
				return errors.New("thread requires tN")
			}
			selected = fs.Arg(0)
		}
		kept := out.Threads[:0]
		for _, t := range out.Threads {
			if selected != "" && t.Id != selected || *unresolved && (t.Status == v1.ThreadStatus_THREAD_STATUS_RESOLVED || t.Status == v1.ThreadStatus_THREAD_STATUS_WITHDRAWN) {
				continue
			}
			kept = append(kept, t)
		}
		out.Threads = kept
		return rpcEmit(cfg, jsonOut, out)
	}
	if command == "drafts" {
		out, err := cfg.API.Reviews.GetReviewDrafts(ctx, rpcRequest(cfg, &v1.GetReviewDraftsRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, out.Msg)
	}
	if *operation == "" {
		*operation, err = model.NewID()
		if err != nil {
			return err
		}
	}
	anchor := &v1.ReviewAnchor{RevisionId: *revision, FromRevisionId: *from}
	if *path != "" {
		anchor.Path = *path
		switch *side {
		case "old":
			anchor.Side = v1.ReviewSide_REVIEW_SIDE_OLD
		case "new":
			anchor.Side = v1.ReviewSide_REVIEW_SIDE_NEW
		default:
			return usageErr{fmt.Errorf("--side is old or new, not %q", *side)}
		}
		anchor.StartLine = uint32(*line)
		anchor.EndLine = uint32(*end)
	}
	switch command {
	case "comment":
		out, err := cfg.API.Reviews.SaveReviewDraft(ctx, rpcRequest(cfg, &v1.SaveReviewDraftRequest{ItemId: id, OperationId: *operation, Draft: &v1.ReviewDraft{ThreadId: *thread, Anchor: anchor, Body: *message}}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, out.Msg)
	case "publish":
		out, err := cfg.API.Reviews.PublishReviewRound(ctx, rpcRequest(cfg, &v1.PublishReviewRoundRequest{ItemId: id, OperationId: *operation, RevisionId: *revision, FromRevisionId: *from, DraftIds: strings.Split(*drafts, ",")}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, out.Msg)
	case "resolve", "reopen", "withdraw":
		if fs.NArg() != 1 {
			return errors.New("resolution requires tN")
		}
		threads, err := allThreads(ctx, cfg, id)
		if err != nil {
			return err
		}
		found := false
		for _, t := range threads.Threads {
			if t.Id == fs.Arg(0) {
				if anchor.RevisionId == "" {
					anchor = t.Anchor
				}
				found = true
			}
		}
		if !found {
			return errors.New("thread not found")
		}
		resolution := map[string]v1.ThreadResolution{"resolve": v1.ThreadResolution_THREAD_RESOLUTION_RESOLVED, "reopen": v1.ThreadResolution_THREAD_RESOLUTION_OPEN, "withdraw": v1.ThreadResolution_THREAD_RESOLUTION_WITHDRAWN}[command]
		d, err := cfg.API.Reviews.SaveReviewDraft(ctx, rpcRequest(cfg, &v1.SaveReviewDraftRequest{ItemId: id, OperationId: *operation + "-draft", Draft: &v1.ReviewDraft{ThreadId: fs.Arg(0), Anchor: anchor, Body: *message, Resolution: resolution}}))
		if err != nil {
			return err
		}
		out, err := cfg.API.Reviews.PublishReviewRound(ctx, rpcRequest(cfg, &v1.PublishReviewRoundRequest{ItemId: id, OperationId: *operation, RevisionId: anchor.RevisionId, FromRevisionId: anchor.FromRevisionId, DraftIds: []string{d.Msg.Result.DraftId}}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, out.Msg)
	case "reply":
		request := &v1.ReplyReviewThreadsRequest{ItemId: id, OperationId: *operation, RevisionId: *revision, Capture: *capture}
		if *stdin {
			body, err := io.ReadAll(io.LimitReader(cfg.Stdin, 1<<20))
			if err != nil {
				return err
			}
			var input struct {
				Replies []*v1.ReviewReply `json:"replies"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				return err
			}
			request.Replies = input.Replies
		} else {
			request.Replies = []*v1.ReviewReply{{ThreadId: *thread, Body: *message, Addressed: *addressed}}
		}
		if *capture {
			ws, err := cfg.API.Workspaces.GetWorkspace(ctx, rpcRequest(cfg, &v1.GetWorkspaceRequest{ItemId: id}))
			if err != nil {
				return err
			}
			claim := ws.Msg.Checkout.GetClaim()
			if claim == nil {
				return errors.New("capture reply requires a checkout")
			}
			request.ExpectedClaimId = claim.Id
			request.ExpectedClaimGeneration = claim.Generation
		}
		out, err := cfg.API.Reviews.ReplyReviewThreads(ctx, rpcRequest(cfg, request))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, out.Msg)
	}
	return fmt.Errorf("unknown review command %q", command)
}
