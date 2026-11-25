package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// One item's agent asks another's with wi send --wait: the message reaches
// the other agent in an envelope naming its sender, the answer is the
// command's result, and the receiving item's history shows the sender and
// the text without the envelope.
func TestAgentsAskEachOtherThroughWiSend(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	var asker, answerer v1.CreateAndStartItemResponse
	f.json(&asker, "new", "--repo", repo.Repository.Id, "--title", "Asker", "--start", "--agent-mode", "headless", "--prompt", "you will ask")
	f.json(&answerer, "new", "--repo", repo.Repository.Id, "--title", "Answerer", "--start", "--agent-mode", "headless", "--prompt", "you will answer")
	f.idle(asker.Item.Id)
	f.idle(answerer.Item.Id)
	credentials, _ := filepath.Glob(filepath.Join(f.root, "state", "wi", "*", "items", asker.Item.Id, "runtimes", "*", "credential"))
	if len(credentials) != 1 {
		t.Fatalf("credentials: %v", credentials)
	}
	cmd := exec.Command(f.wi, "send", answerer.Item.Id, "--wait", "--timeout", "20s", "Do the checks pass?")
	cmd.Env = append(append([]string{}, f.env...), "WI_ID="+asker.Item.Id, "WI_ITEM_CREDENTIAL_FILE="+credentials[0])
	cmd.Dir = f.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wi send --wait: %v %s", err, out)
	}
	// The test agent echoes what it received: the envelope wi wrote.
	answer := string(out)
	if !strings.Contains(answer, "Echo: <wi-message-") || !strings.Contains(answer, `from="item:`+asker.Item.Id+`"`) || !strings.Contains(answer, `title="Asker"`) || !strings.Contains(answer, `hops="1"`) || !strings.Contains(answer, "Do the checks pass?") {
		t.Fatalf("answer: %s", answer)
	}
	history, err := f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: answerer.Item.Id}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range history.Msg.Messages {
		if m.Sender == "item:"+asker.Item.Id {
			found = true
			if text := m.Parts[0].GetText(); text != "Do the checks pass?" {
				t.Fatalf("history kept the envelope: %q", text)
			}
		}
	}
	if !found {
		t.Fatalf("no message from the asker: %v", history.Msg.Messages)
	}
	// A person's message names them.
	f.run("send", answerer.Item.Id, "--wait", "From you")
	history, _ = f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: answerer.Item.Id}))
	last := ""
	for _, m := range history.Msg.Messages {
		if m.Role == v1.MessageRole_MESSAGE_ROLE_USER && m.Sender != "" {
			last = m.Sender
		}
	}
	if last != "user:operator" {
		t.Fatalf("the operator's message sender: %q", last)
	}
}
