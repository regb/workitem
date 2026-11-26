package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// A delegation's attention goes to the item that owns it: when its turn
// ends or it waits on a question, the owning item's agent gets a notice
// from it. A turn the owner waits on with wi send --wait sends none.
func TestOwningItemsAreNoticedOfTheirDelegations(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	var parent v1.CreateAndStartItemResponse
	f.json(&parent, "new", "--repo", repo.Repository.Id, "--title", "Parent", "--start", "--agent-mode", "headless", "--prompt", "you will delegate")
	f.idle(parent.Item.Id)
	credentials, _ := filepath.Glob(filepath.Join(f.root, "state", "wi", "*", "items", parent.Item.Id, "runtimes", "*", "credential"))
	if len(credentials) != 1 {
		t.Fatalf("credentials: %v", credentials)
	}
	asParent := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(f.wi, args...)
		cmd.Env = append(append([]string{}, f.env...), "WI_ID="+parent.Item.Id, "WI_ITEM_CREDENTIAL_FILE="+credentials[0])
		cmd.Dir = f.root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wi %v as the parent: %v %s", args, err, out)
		}
		return out
	}
	notices := func() []string {
		t.Helper()
		history, err := f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: parent.Item.Id}))
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, m := range history.Msg.Messages {
			if m.Role == v1.MessageRole_MESSAGE_ROLE_USER && len(m.Parts) > 0 && strings.HasPrefix(m.Parts[0].GetText(), "wi notice:") {
				out = append(out, m.Parts[0].GetText())
			}
		}
		return out
	}
	await := func(count int, contains string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			got := notices()
			if len(got) >= count && strings.Contains(got[count-1], contains) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("notices: %q", got)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	// The parent delegates with a task: the child's first turn ends, and the
	// parent hears of it with the child's reply.
	var child v1.CreateAndStartItemResponse
	if err := protojson.Unmarshal(asParent("new", "--title", "Child", "--start", "--agent-mode", "headless", "--prompt", "do the thing", "--json"), &child); err != nil {
		t.Fatal(err)
	}
	await(1, "finished its turn")
	if got := notices()[0]; !strings.Contains(got, child.Item.Id) || !strings.Contains(got, "do the thing") {
		t.Fatalf("notice: %s", got)
	}
	f.idle(parent.Item.Id)
	// Waiting on the reply makes it the command's result, not a notice.
	if out := asParent("send", child.Item.Id, "--wait", "ping"); !strings.Contains(string(out), "ping") {
		t.Fatalf("waited answer: %s", out)
	}
	time.Sleep(2 * time.Second)
	if got := notices(); len(got) != 1 {
		t.Fatalf("a waited turn was noticed: %q", got)
	}
	// A question is noticed too.
	f.run("send", child.Item.Id, "please ask me something")
	await(2, "waiting on a question")
}
