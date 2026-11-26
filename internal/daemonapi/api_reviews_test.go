package daemonapi_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"google.golang.org/protobuf/encoding/protojson"
)

// reviewsGitRoot is the repository's operational Git directory, shared by
// every checkout of it.
func reviewsGitRoot(t *testing.T, checkout *v1.Checkout) string {
	t.Helper()
	root := git(t, checkout.Claim.Path, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(root) {
		root = filepath.Join(checkout.Claim.Path, root)
	}
	return root
}

// reviewsCapture captures a checkpoint of item's checkout.
func reviewsCapture(t *testing.T, f *apitest.Fixture, item string, claim *v1.WorkspaceClaim, id string) *v1.CaptureReviewRevisionResponse {
	t.Helper()
	return call(t, f.Clients.Reviews.CaptureReviewRevision, &v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: id, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation})
}

// Checkpoints are immutable snapshots: later edits, a release and Git's
// garbage collection leave them readable, and deleting the item removes them.
func TestReviewsCheckpoints(t *testing.T) {
	t.Parallel()
	f, item, checkout := workspacesCheckout(t)
	claim := checkout.Claim
	root := reviewsGitRoot(t, checkout)
	repository := call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: item}).Item.Repository.Id
	workspacesWrite(t, checkout, "example.go", "first\n")
	one := reviewsCapture(t, f, item, claim, "capture-one")
	if one.Revision.Id != "r1" || one.Version == 0 || one.Revision.BaseCommit != one.Revision.ObservedHead {
		t.Fatalf("first checkpoint, based on the observed HEAD: %v", one)
	}
	t.Run("a capture ID is idempotent", func(t *testing.T) {
		replay := reviewsCapture(t, f, item, claim, "capture-one")
		if replay.Revision.Commit != one.Revision.Commit || !replay.Reused {
			t.Fatalf("replay: %v", replay)
		}
		if same := reviewsCapture(t, f, item, claim, "capture-same-tree"); same.Revision.Id != "r2" || same.Reused {
			t.Fatalf("a new capture of the same tree: %v", same)
		}
	})
	workspacesWrite(t, checkout, "example.go", "second\n")
	two := reviewsCapture(t, f, item, claim, "capture-two")
	if two.Revision.Id != "r3" || two.Revision.Commit == one.Revision.Commit {
		t.Fatalf("second checkpoint: %v", two)
	}
	comparison := &v1.GetRevisionReviewRequest{ItemId: item, RevisionId: "r3", FromRevisionId: "r1"}
	review := call(t, f.Clients.Reviews.GetRevisionReview, comparison)
	file := &v1.GetRevisionReviewFileRequest{Comparison: comparison, Path: "example.go", ExpectedVersion: review.Review.Revision.Version}
	immutable := func(t *testing.T) {
		t.Helper()
		got := call(t, f.Clients.Reviews.GetRevisionReviewFile, file)
		if got.File.OldFile.Content != "first\n" || got.File.NewFile.Content != "second\n" {
			t.Fatalf("comparison: %v", got.File)
		}
		patch := call(t, f.Clients.Reviews.GetRevisionPatch, &v1.GetRevisionPatchRequest{Comparison: comparison})
		if !strings.Contains(patch.Patch, "-first") || !strings.Contains(patch.Patch, "+second") || patch.Version == "" {
			t.Fatalf("patch: %v", patch)
		}
	}
	t.Run("comparisons ignore later edits", func(t *testing.T) {
		workspacesWrite(t, checkout, "example.go", "later unsaved edits\n")
		immutable(t)
	})
	t.Run("captures are fenced by the claim", func(t *testing.T) {
		refused(t, connect.CodeAborted, f.Clients.Reviews.CaptureReviewRevision, &v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "stale", ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation + 1})
	})
	t.Run("release and garbage collection keep the history", func(t *testing.T) {
		git(t, claim.Path, "clean", "-fdq")
		call(t, f.Clients.Workspaces.ReleaseWorkspace, &v1.ReleaseWorkspaceRequest{ItemId: item})
		git(t, root, "gc", "--prune=now")
		immutable(t)
		if listed := call(t, f.Clients.Reviews.ListReviewRevisions, &v1.ListReviewRevisionsRequest{ItemId: item}); len(listed.Revisions) != 3 {
			t.Fatalf("revisions: %v", listed)
		}
	})
	t.Run("deleting the item deletes its review", func(t *testing.T) {
		call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: item, State: v1.ItemState_ITEM_STATE_CLOSED, Resolution: v1.Resolution_RESOLUTION_COMPLETED})
		call(t, f.Clients.Items.DeleteItem, &v1.DeleteItemRequest{ItemId: item, Confirm: true})
		refused(t, connect.CodeNotFound, f.Clients.Reviews.ListReviewRevisions, &v1.ListReviewRevisionsRequest{ItemId: item})
		if refs := git(t, root, "for-each-ref", "--format=%(refname)", "refs/wi/review/"+item+"/"); refs != "" {
			t.Fatalf("deletion retained review refs: %s", refs)
		}
	})
	t.Run("a snapshot left behind is a partial outcome", func(t *testing.T) {
		other, otherCheckout := activeItem(t, f, repository, "Leftover snapshot")
		workspacesWrite(t, otherCheckout, "review.txt", "snapshot\n")
		captured := reviewsCapture(t, f, other.Id, otherCheckout.Claim, "delete-test")
		git(t, otherCheckout.Claim.Path, "clean", "-fdq")
		call(t, f.Clients.Workspaces.ReleaseWorkspace, &v1.ReleaseWorkspaceRequest{ItemId: other.Id})
		call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: other.Id, State: v1.ItemState_ITEM_STATE_CLOSED, Resolution: v1.Resolution_RESOLUTION_COMPLETED})
		// A held ref lock makes Git refuse to delete the snapshot.
		lock := filepath.Join(root, filepath.FromSlash(captured.Revision.Ref)+".lock")
		if err := os.WriteFile(lock, nil, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(lock)
		_, err := f.Clients.Items.DeleteItem(t.Context(), connect.NewRequest(&v1.DeleteItemRequest{ItemId: other.Id, Confirm: true}))
		if partial := reviewsPartialOutcome(err); partial == nil || len(partial.Completed) == 0 {
			t.Fatalf("deletion did not report its completed steps: %v", err)
		}
		refused(t, connect.CodeNotFound, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: other.Id})
		refused(t, connect.CodeNotFound, f.Clients.Reviews.ListReviewRevisions, &v1.ListReviewRevisionsRequest{ItemId: other.Id})
		if ref := git(t, root, "rev-parse", captured.Revision.Ref); ref != captured.Revision.Commit {
			t.Fatalf("the snapshot ref left behind: %s", ref)
		}
	})
}

// reviewsPartialOutcome is the partial outcome an error carries, if any.
func reviewsPartialOutcome(err error) *v1.PartialOutcome {
	var failure *connect.Error
	if !errors.As(err, &failure) {
		return nil
	}
	for _, detail := range failure.Details() {
		if value, err := detail.Value(); err == nil {
			if partial, ok := value.(*v1.PartialOutcome); ok {
				return partial
			}
		}
	}
	return nil
}

// A reviewer drafts, publishes a round, and the agent replies on a new
// checkpoint; every step is one atomic record, and item events never carry
// the review's text.
func TestReviewsRoundsRepliesAndIntent(t *testing.T) {
	t.Parallel()
	f, item, checkout := workspacesCheckout(t)
	claim := checkout.Claim
	workspacesWrite(t, checkout, "example.txt", "one\n")
	reviewsCapture(t, f, item, claim, "one")
	anchor := &v1.ReviewAnchor{RevisionId: "r1", Path: "example.txt", Side: v1.ReviewSide_REVIEW_SIDE_NEW, StartLine: 1}
	draft := call(t, f.Clients.Reviews.SaveReviewDraft, &v1.SaveReviewDraftRequest{ItemId: item, OperationId: "draft", Draft: &v1.ReviewDraft{Anchor: anchor, Body: "private reviewer comment text"}}).Result.DraftId
	t.Run("drafts are listed and deleted", func(t *testing.T) {
		discard := call(t, f.Clients.Reviews.SaveReviewDraft, &v1.SaveReviewDraftRequest{ItemId: item, OperationId: "discard", Draft: &v1.ReviewDraft{Anchor: anchor, Body: "second thoughts"}}).Result.DraftId
		drafts := call(t, f.Clients.Reviews.GetReviewDrafts, &v1.GetReviewDraftsRequest{ItemId: item})
		if len(drafts.Drafts) != 2 {
			t.Fatalf("drafts: %v", drafts)
		}
		refused(t, connect.CodeAborted, f.Clients.Reviews.DeleteReviewDrafts, &v1.DeleteReviewDraftsRequest{ItemId: item, OperationId: "stale", DraftIds: []string{discard}, ExpectedVersion: drafts.Version + 1})
		call(t, f.Clients.Reviews.DeleteReviewDrafts, &v1.DeleteReviewDraftsRequest{ItemId: item, OperationId: "delete", DraftIds: []string{discard}, ExpectedVersion: drafts.Version})
		if left := call(t, f.Clients.Reviews.GetReviewDrafts, &v1.GetReviewDraftsRequest{ItemId: item}); len(left.Drafts) != 1 || left.Drafts[0].Id != draft {
			t.Fatalf("drafts after deletion: %v", left)
		}
	})
	publish := &v1.PublishReviewRoundRequest{ItemId: item, OperationId: "publish", RevisionId: "r1", DraftIds: []string{draft}}
	t.Run("a round is published even when no agent can be notified", func(t *testing.T) {
		published := call(t, f.Clients.Reviews.PublishReviewRound, publish)
		if published.Result.RoundId != "q1" || len(published.Warnings) != 1 || !strings.Contains(published.Warnings[0], "review round q1 is published, but its agent was not told") {
			t.Fatalf("publishing with no agent running: %v", published)
		}
		threads := call(t, f.Clients.Reviews.GetReviewThreads, &v1.GetReviewThreadsRequest{ItemId: item})
		if len(threads.Threads) != 1 || threads.Threads[0].Anchor.Path != "example.txt" || len(threads.Rounds) != 1 || threads.Rounds[0].Id != "q1" || threads.Rounds[0].NotifiedAt != nil || threads.Rounds[0].CommandId != "" {
			t.Fatalf("published round: %v", threads)
		}
		if drafts := call(t, f.Clients.Reviews.GetReviewDrafts, &v1.GetReviewDraftsRequest{ItemId: item}); len(drafts.Drafts) != 0 {
			t.Fatalf("published drafts remain: %v", drafts)
		}
		if again := call(t, f.Clients.Reviews.PublishReviewRound, publish); !again.Result.Reused {
			t.Fatalf("retry: %v", again)
		}
		if _, err := f.Clients.Reviews.NotifyReviewRound(t.Context(), connect.NewRequest(&v1.NotifyReviewRoundRequest{ItemId: item, RoundId: "q1"})); err == nil {
			t.Fatal("notifying started an agent")
		}
	})
	t.Run("an invalid reply batch changes nothing", func(t *testing.T) {
		_, err := f.Clients.Reviews.ReplyReviewThreads(t.Context(), connect.NewRequest(&v1.ReplyReviewThreadsRequest{ItemId: item, OperationId: "bad", Capture: true, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation, Replies: []*v1.ReviewReply{{ThreadId: "missing", Body: "bad"}}}))
		if err == nil {
			t.Fatal("invalid batch accepted")
		}
		threads := call(t, f.Clients.Reviews.GetReviewThreads, &v1.GetReviewThreadsRequest{ItemId: item})
		revisions := call(t, f.Clients.Reviews.ListReviewRevisions, &v1.ListReviewRevisionsRequest{ItemId: item})
		if len(threads.Threads[0].Messages) != 1 || len(revisions.Revisions) != 1 {
			t.Fatalf("invalid batch published half: %v %v", threads, revisions)
		}
	})
	t.Run("a reply captures the fix and keeps the reviewer's anchor", func(t *testing.T) {
		workspacesWrite(t, checkout, "example.txt", "fixed\n")
		reply := &v1.ReplyReviewThreadsRequest{ItemId: item, OperationId: "fix", Capture: true, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation, Replies: []*v1.ReviewReply{{ThreadId: "t1", Body: "Fixed on new revision", Addressed: true}}}
		fixed := call(t, f.Clients.Reviews.ReplyReviewThreads, reply)
		if fixed.Result.CapturedRevision.GetId() != "r2" {
			t.Fatalf("reply capture: %v", fixed)
		}
		if again := call(t, f.Clients.Reviews.ReplyReviewThreads, reply); !again.Result.Reused || again.Result.CapturedRevision.GetCommit() != fixed.Result.CapturedRevision.Commit {
			t.Fatalf("retry recaptured: %v", again)
		}
		thread := call(t, f.Clients.Reviews.GetReviewThreads, &v1.GetReviewThreadsRequest{ItemId: item}).Threads[0]
		if thread.Status != v1.ThreadStatus_THREAD_STATUS_ADDRESSED || thread.Anchor.RevisionId != "r1" || len(thread.Messages) != 2 || thread.Messages[1].RevisionId != "r2" || thread.Messages[1].Role != v1.ReviewRole_REVIEW_ROLE_AGENT {
			t.Fatalf("thread after reply: %v", thread)
		}
	})
	t.Run("approval does not wait on open concerns", func(t *testing.T) {
		before := call(t, f.Clients.Reviews.GetReviewIntent, &v1.GetReviewIntentRequest{ItemId: item, RevisionId: "r2"})
		if before.Approved {
			t.Fatalf("approved before approval: %v", before)
		}
		call(t, f.Clients.Reviews.SetReviewIntent, &v1.SetReviewIntentRequest{ItemId: item, OperationId: "approve", RevisionId: "r2", Kind: v1.ApprovalKind_APPROVAL_KIND_APPROVE, ExpectedVersion: before.Version})
		after := call(t, f.Clients.Reviews.GetReviewIntent, &v1.GetReviewIntentRequest{ItemId: item, RevisionId: "r2"})
		if !after.Approved || len(after.Approvals) != 1 || after.Approvals[0].Kind != v1.ApprovalKind_APPROVAL_KIND_APPROVE {
			t.Fatalf("intent: %v", after)
		}
		call(t, f.Clients.Reviews.SetReviewIntent, &v1.SetReviewIntentRequest{ItemId: item, OperationId: "withdraw", RevisionId: "r2", Kind: v1.ApprovalKind_APPROVAL_KIND_WITHDRAW_APPROVAL})
		if withdrawn := call(t, f.Clients.Reviews.GetReviewIntent, &v1.GetReviewIntentRequest{ItemId: item, RevisionId: "r2"}); withdrawn.Approved {
			t.Fatalf("withdrawn approval stands: %v", withdrawn)
		}
	})
	t.Run("Git keeps only snapshots, events no text", func(t *testing.T) {
		for _, ref := range strings.Fields(git(t, reviewsGitRoot(t, checkout), "for-each-ref", "--format=%(refname)", "refs/wi/review/"+item+"/")) {
			if !strings.Contains(ref, "/snapshots/") {
				t.Fatalf("review metadata in Git: %s", ref)
			}
		}
		events := call(t, f.Clients.Items.ListItemEvents, &v1.ListItemEventsRequest{ItemId: item})
		body, err := protojson.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "private reviewer comment text") || strings.Contains(string(body), "Fixed on new revision") {
			t.Fatal("an item event carries review text")
		}
	})
}

// Reviews of the working tree, of commits and of the context around the
// item's branch, with and without a checkout.
func TestReviewsComparisonsAndCommitGraph(t *testing.T) {
	t.Parallel()
	f, item, checkout := workspacesCheckout(t)
	odd := "source with\nnewline.go"
	workspacesWrite(t, checkout, odd, "edited\n")
	workspacesWrite(t, checkout, ".gitignore", "ignored\n")
	var committed string
	t.Run("the working tree and its commit", func(t *testing.T) {
		comparison := v1.ReviewComparison_REVIEW_COMPARISON_ITEM
		review := call(t, f.Clients.Reviews.GetReview, &v1.GetReviewRequest{ItemId: item, Comparison: comparison})
		if len(review.Files) != 2 || !review.Revision.WorkingTree {
			t.Fatalf("review: %v", review)
		}
		file := &v1.GetReviewFileRequest{ItemId: item, Comparison: comparison, Path: odd, ExpectedVersion: review.Revision.Version}
		if diff := call(t, f.Clients.Reviews.GetReviewFile, file); diff.OldFile.Exists || diff.NewFile.Content != "edited\n" {
			t.Fatalf("diff: %v", diff)
		}
		committed = call(t, f.Clients.Workspaces.CommitWorkspace, &v1.CommitWorkspaceRequest{ItemId: item, Message: "Add reviewed files"}).Commit
		refused(t, connect.CodeAborted, f.Clients.Reviews.GetReviewFile, file)
		history := call(t, f.Clients.Reviews.ListReviewCommits, &v1.ListReviewCommitsRequest{ItemId: item})
		if len(history.Commits) != 1 || history.Commits[0].Sha != committed {
			t.Fatalf("history: %v", history)
		}
		if selected := call(t, f.Clients.Reviews.GetReview, &v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: committed}); len(selected.Files) != 2 {
			t.Fatalf("commit review: %v", selected)
		}
		for _, sha := range []string{"--all", "HEAD", "0000000000000000000000000000000000000000"} {
			if _, err := f.Clients.Reviews.GetReview(t.Context(), connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: sha})); err == nil {
				t.Fatalf("accepted invalid commit %s", sha)
			}
		}
	})
	root := reviewsGitRoot(t, checkout)
	t.Run("context stays within three branches", func(t *testing.T) {
		integration := git(t, root, "symbolic-ref", "--short", "HEAD")
		base := git(t, root, "rev-parse", integration)
		tree := git(t, root, "rev-parse", base+"^{tree}")
		local := git(t, root, "commit-tree", tree, "-p", base, "-m", "local integration work")
		remote := git(t, root, "commit-tree", tree, "-p", base, "-m", "remote-only work")
		other := git(t, root, "commit-tree", tree, "-p", base, "-m", "unrelated work")
		git(t, root, "update-ref", "refs/heads/"+integration, local)
		git(t, root, "update-ref", "refs/heads/unrelated", other)
		git(t, root, "update-ref", "refs/remotes/upstream/main", remote)
		git(t, root, "remote", "add", "upstream", "https://example.invalid/no-network.git")
		git(t, root, "config", "branch."+integration+".remote", "upstream")
		git(t, root, "config", "branch."+integration+".merge", "refs/heads/main")
		if own := call(t, f.Clients.Reviews.ListReviewCommits, &v1.ListReviewCommitsRequest{ItemId: item}); len(own.Commits) != 1 {
			t.Fatalf("the item's own history changed: %v", own)
		}
		graph := call(t, f.Clients.Reviews.ListReviewCommits, &v1.ListReviewCommitsRequest{ItemId: item, IncludeContext: true})
		if len(graph.Branches) != 3 {
			t.Fatalf("graph branches: %v", graph.Branches)
		}
		var shas []string
		for _, commit := range graph.Commits {
			shas = append(shas, commit.Sha)
		}
		for _, sha := range []string{base, local, remote, committed} {
			if !slices.Contains(shas, sha) {
				t.Fatalf("graph lacks %s: %v", sha, graph)
			}
		}
		if slices.Contains(shas, other) {
			t.Fatal("unrelated branch included")
		}
		for _, sha := range []string{base, local, remote} {
			call(t, f.Clients.Reviews.GetReview, &v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: sha})
		}
		custom := call(t, f.Clients.Reviews.GetReview, &v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: local, BaseCommit: remote})
		if custom.Revision.BaseCommit != remote || custom.Revision.HeadCommit != local {
			t.Fatalf("custom range: %v", custom.Revision)
		}
		standard := call(t, f.Clients.Reviews.GetReview, &v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: local})
		file := &v1.GetReviewFileRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: local, BaseCommit: remote, Path: "missing.txt", ExpectedVersion: standard.Revision.Version}
		refused(t, connect.CodeAborted, f.Clients.Reviews.GetReviewFile, file)
		file.ExpectedVersion = custom.Revision.Version
		call(t, f.Clients.Reviews.GetReviewFile, file)
		for _, bad := range []string{other, "HEAD", "--all"} {
			if _, err := f.Clients.Reviews.GetReview(t.Context(), connect.NewRequest(&v1.GetReviewRequest{ItemId: item, BaseCommit: bad})); err == nil {
				t.Fatalf("unapproved base accepted: %s", bad)
			}
		}
		refused(t, connect.CodeFailedPrecondition, f.Clients.Reviews.GetReview, &v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: other})
	})
	t.Run("reviews read the branch without a checkout", func(t *testing.T) {
		call(t, f.Clients.Workspaces.ReleaseWorkspace, &v1.ReleaseWorkspaceRequest{ItemId: item})
		bare := call(t, f.Clients.Reviews.GetReview, &v1.GetReviewRequest{ItemId: item})
		if bare.Revision.WorkingTree || len(bare.Files) != 2 {
			t.Fatalf("review without checkout: %v", bare)
		}
		if diff := call(t, f.Clients.Reviews.GetReviewFile, &v1.GetReviewFileRequest{ItemId: item, Path: odd, ExpectedVersion: bare.Revision.Version}); diff.NewFile.Content != "edited\n" {
			t.Fatalf("diff without checkout: %v", diff)
		}
		if graph := call(t, f.Clients.Reviews.ListReviewCommits, &v1.ListReviewCommitsRequest{ItemId: item, IncludeContext: true}); len(graph.Branches) != 3 {
			t.Fatalf("graph without checkout: %v", graph)
		}
		refused(t, connect.CodeFailedPrecondition, f.Clients.Reviews.GetReview, &v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_UNCOMMITTED})
	})
}

// A published round reaches the item's agent once: as a prompt when it is
// idle, as a follow-up when it is busy. Comment bodies stay out of the chat;
// an explicit notification sends it again.
func TestReviewsNotifyTheAgent(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	repository := f.Repository(t, "test")
	for _, busy := range []bool{false, true} {
		name := map[bool]string{false: "idle", true: "busy"}[busy]
		t.Run(name, func(t *testing.T) {
			item, checkout := activeItem(t, f, repository, name+" review")
			startAgent(t, f, item.Id)
			expected := 4
			if busy {
				call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item.Id, Text: "hold busy agent", Delivery: v1.Delivery_DELIVERY_PROMPT})
				apitest.WaitHistory(t, f.Clients, item.Id, 1)
				expected = 3
			} else {
				send(t, f, item.Id, "initial prompt")
			}
			workspacesWrite(t, checkout, "review.txt", "code\n")
			revision := reviewsCapture(t, f, item.Id, checkout.Claim, "snapshot").Revision.Id
			draft := call(t, f.Clients.Reviews.SaveReviewDraft, &v1.SaveReviewDraftRequest{ItemId: item.Id, OperationId: "draft", Draft: &v1.ReviewDraft{Anchor: &v1.ReviewAnchor{RevisionId: revision}, Body: "private concern not copied to chat"}}).Result.DraftId
			publish := &v1.PublishReviewRoundRequest{ItemId: item.Id, OperationId: "publish", RevisionId: revision, DraftIds: []string{draft}}
			if round := call(t, f.Clients.Reviews.PublishReviewRound, publish); round.Result.RoundId != "q1" {
				t.Fatalf("round: %v", round)
			}
			history := apitest.WaitHistory(t, f.Clients, item.Id, expected)
			apitest.WaitIdle(t, f.Clients, item.Id)
			round := call(t, f.Clients.Reviews.GetReviewThreads, &v1.GetReviewThreadsRequest{ItemId: item.Id}).Rounds[0]
			if round.NotifiedAt == nil || round.CommandId == "" {
				t.Fatalf("notification receipt: %v", round)
			}
			var chat strings.Builder
			for _, message := range history {
				chat.WriteString(messageText(message))
			}
			if !strings.Contains(chat.String(), "round q1") || strings.Contains(chat.String(), "private concern not copied to chat") {
				t.Fatalf("notification: %s", chat.String())
			}
			if again := call(t, f.Clients.Reviews.PublishReviewRound, publish); !again.Result.Reused {
				t.Fatalf("retry: %v", again)
			}
			notified := call(t, f.Clients.Reviews.NotifyReviewRound, &v1.NotifyReviewRoundRequest{ItemId: item.Id, RoundId: "q1", ClientMessageId: "explicit-resend"})
			if notified.Round.CommandId == round.CommandId {
				t.Fatal("an explicit notification reused the first command")
			}
			apitest.WaitHistory(t, f.Clients, item.Id, expected+2)
			apitest.WaitIdle(t, f.Clients, item.Id)
			// The publication retry sent nothing: only the explicit
			// notification and its answer followed the first one.
			if final := call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item.Id}); len(final.Messages) != expected+2 {
				t.Fatalf("history after one explicit notification: %d messages, want %d", len(final.Messages), expected+2)
			}
		})
	}
}
