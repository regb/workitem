package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

func rpcReview(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	command := "revisions"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "threads", "thread", "drafts", "comment", "publish", "reply", "resolve", "reopen", "withdraw":
		return rpcReviewDiscussion(ctx, command, args, cfg, jsonOut)
	case "intent", "approve", "request-review", "request-changes", "withdraw-approval", "notify":
		return rpcReviewIntent(ctx, command, args, cfg, jsonOut)
	case "capture-status":
		return rpcReviewCaptureStatus(ctx, args, cfg, jsonOut)
	case "checkpoint", "revisions", "status", "diff":
	default:
		return fmt.Errorf("unknown review command %q", command)
	}
	fs := flag.NewFlagSet("review "+command, flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	item := fs.String("item", "", "work item")
	capture := fs.String("capture-id", "", "stable capture identity")
	patch := fs.Bool("patch", false, "show patch")
	stat := fs.Bool("stat", false, "list changed files")
	path := fs.String("path", "", "literal file path")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	var flagErr error
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "capture-id" && command != "checkpoint" || (f.Name == "path" || f.Name == "patch" || f.Name == "stat") && command != "diff" {
			flagErr = fmt.Errorf("--%s is not supported for %s", f.Name, command)
		}
	})
	if flagErr != nil {
		return flagErr
	}
	if *patch && *stat {
		return errors.New("choose --patch or --stat")
	}
	if *path != "" && !*patch {
		return errors.New("--path requires --patch")
	}
	id, err := rpcSelect(ctx, cfg, *item)
	if err != nil {
		return err
	}
	if command == "checkpoint" {
		workspace, err := cfg.API.Workspaces.GetWorkspace(ctx, rpcRequest(cfg, &v1.GetWorkspaceRequest{ItemId: id}))
		if err != nil {
			return err
		}
		claim := workspace.Msg.Checkout.GetClaim()
		if claim == nil {
			return errors.New("item has no checkout")
		}
		if *capture == "" {
			*capture, err = model.NewID()
			if err != nil {
				return err
			}
		}
		out, err := cfg.API.Reviews.CaptureReviewRevision(ctx, rpcRequest(cfg, &v1.CaptureReviewRevisionRequest{ItemId: id, CaptureId: *capture, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, out.Msg)
		}
		fmt.Fprintf(cfg.Stdout, "Captured %s at %s\n", out.Msg.Revision.Id, out.Msg.Revision.Commit)
		return nil
	}
	list, err := allRevisions(ctx, cfg, id)
	if err != nil {
		return err
	}
	if command == "revisions" || command == "status" {
		if fs.NArg() != 0 {
			return errors.New("review listing takes no positional arguments")
		}
		if jsonOut {
			return rpcEmit(cfg, true, list)
		}
		for _, r := range list.Revisions {
			fmt.Fprintf(cfg.Stdout, "%s  %s  %s\n", r.Id, r.Commit, r.ObservedBranch)
		}
		return nil
	}
	if command != "diff" {
		return fmt.Errorf("unknown review command %q", command)
	}
	if len(list.Revisions) == 0 {
		return errors.New("no captured revision")
	}
	target := list.Revisions[len(list.Revisions)-1].Id
	from := ""
	if fs.NArg() == 1 {
		target = fs.Arg(0)
	} else if fs.NArg() == 2 {
		from, target = fs.Arg(0), fs.Arg(1)
	} else if fs.NArg() > 2 {
		return errors.New("usage: wi review diff [FROM] [TO] [--patch]")
	}
	comparison := &v1.GetRevisionReviewRequest{ItemId: id, RevisionId: target, FromRevisionId: from}
	if *patch {
		out, err := cfg.API.Reviews.GetRevisionPatch(ctx, rpcRequest(cfg, &v1.GetRevisionPatchRequest{Comparison: comparison, Path: *path}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, out.Msg)
		}
		fmt.Fprint(cfg.Stdout, out.Msg.Patch)
		return nil
	}
	out, err := cfg.API.Reviews.GetRevisionReview(ctx, rpcRequest(cfg, comparison))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, out.Msg)
	}
	for _, f := range out.Msg.Review.Files {
		fmt.Fprintf(cfg.Stdout, "%s  %s\n", f.Change, f.Path)
	}
	return nil
}
