package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/regb/workitem/internal/plugin"
	webaccess "github.com/regb/workitem/internal/web"
)

// A web request never asks the server's terminal: without the page's answer
// the approval is refused and reported, so the page can ask and resend.
func TestWebApprovalsAskThePage(t *testing.T) {
	request := plugin.Approval{ItemID: "item", Path: "/w/.envrc", Question: "Allow worktree .envrc?"}
	ctx, report := webApprovals(context.Background(), "")
	approved, err := plugin.ContextApprover(ctx)(ctx, request)
	var required *plugin.ApprovalRequired
	if approved || !errors.As(err, &required) {
		t.Fatalf("approved=%v err=%v", approved, err)
	}
	var out webaccess.Outcome
	report(&out)
	if out.Approval == nil || out.Approval.Path != "/w/.envrc" || out.Error == "" {
		t.Fatalf("outcome %+v", out)
	}
	for answer, want := range map[string]bool{"allow": true, "deny": false} {
		ctx, report := webApprovals(context.Background(), answer)
		approved, err := plugin.ContextApprover(ctx)(ctx, request)
		var out webaccess.Outcome
		report(&out)
		if approved != want || err != nil || out.Approval != nil {
			t.Fatalf("%s: approved=%v err=%v outcome=%+v", answer, approved, err, out)
		}
	}
}
