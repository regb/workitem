package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestMergeAllowsIdleOnlineAgentAndRefusesRunningTurn(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	var started v1.CreateAndStartItemResponse
	f.json(&started, "new", "--repo", repo.Repository.Id, "--title", "Idle merge", "--start", "--agent-mode", "headless", "--prompt", "first prompt")
	item := started.Item.Id
	agent := f.idle(item)
	generation := agent.Generation
	deadline := time.Now().Add(5 * time.Second)
	for {
		shown, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		if !shown.Msg.Observation.Capturing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("settlement capture did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ws, err := f.clients.Workspaces.GetWorkspace(ctx, connect.NewRequest(&v1.GetWorkspaceRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range [][]string{{"user.name", "Merge test"}, {"user.email", "merge@example.invalid"}} {
		cmd := exec.Command("git", "-C", ws.Msg.Checkout.Claim.Path, "config", setting[0], setting[1])
		cmd.Env = f.env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git identity: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(ws.Msg.Checkout.Claim.Path, "idle-work.txt"), []byte("merge with idle online agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var merged v1.IntegrateItemResponse
	f.json(&merged, "merge", "--item", item, "--local")
	if merged.Job.State != v1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatal(merged.String())
	}
	agent = f.idle(item)
	if !agent.Online || agent.Generation != generation {
		t.Fatal("merge stopped or replaced the idle agent", agent)
	}
	f.run("send", "--item", item, "hold merge running turn")
	deadline = time.Now().Add(5 * time.Second)
	for {
		current, err := f.clients.Agents.GetAgent(ctx, connect.NewRequest(&v1.GetAgentRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		if current.Msg.Agent.Status == v1.AgentStatus_AGENT_STATUS_BUSY {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if out, err := f.command("merge", "--item", item, "--local"); err == nil || !strings.Contains(string(out), "busy running a turn") {
		t.Fatalf("busy merge not refused clearly: %v %s", err, out)
	}
	// The item's own agent merges from inside its turn: its credential, in
	// the file its environment names, proves it is the item's current
	// runtime. The daemon writes the file beside the runtime's log.
	credentials, _ := filepath.Glob(filepath.Join(f.root, "state", "wi", "*", "items", item, "runtimes", "*", "credential"))
	if len(credentials) != 1 {
		t.Fatalf("credentials: %v", credentials)
	}
	asAgent := func(file string) ([]byte, error) {
		cmd := exec.Command(f.wi, "merge", "--item", item, "--local")
		cmd.Env = append(append([]string{}, f.env...), "WI_ID="+item, "WI_ITEM_CREDENTIAL_FILE="+file)
		cmd.Dir = f.root
		return cmd.CombinedOutput()
	}
	forged := filepath.Join(f.root, "forged-credential")
	if err := os.WriteFile(forged, []byte("wi_item_"+item+"_"+strings.Repeat("A", 43)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := asAgent(forged); err == nil || !strings.Contains(string(out), "no longer valid") {
		t.Fatalf("a forged credential merged: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(ws.Msg.Checkout.Claim.Path, "agent-work.txt"), []byte("merged by its own agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := asAgent(credentials[0]); err != nil {
		t.Fatalf("the agent's own merge from inside its turn: %v %s", err, out)
	}
}
