package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// This server can only address the fixture's TMUX_TMPDIR, never the operator's.
func (f *binaryFixture) tui(repository string) {
	f.t.Run("isolated_TUI_and_control_replacement", func(t *testing.T) {
		copy := *f
		copy.t = t
		f := &copy
		if _, err := exec.LookPath("tmux"); err != nil {
			t.Skip("tmux unavailable")
		}
		socket := filepath.Join(f.root, "tmux", fmt.Sprintf("tmux-%d", os.Getuid()), "default")
		inventory := exec.Command("tmux", "-S", socket, "list-sessions")
		inventory.Env = f.env
		if out, err := inventory.CombinedOutput(); err == nil {
			t.Fatalf("unexpected existing test server: %s", out)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		wait := func(job *v1.Job) {
			t.Helper()
			stream, err := f.clients.Jobs.WatchJob(ctx, connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var final *v1.Job
			for stream.Receive() {
				final = stream.Msg().Job
			}
			if err := stream.Err(); err != nil || final == nil || final.State != v1.JobState_JOB_STATE_SUCCEEDED {
				t.Fatalf("job: %v %v", final, err)
			}
		}
		profiles, err := f.clients.Profiles.ListAgentProfiles(ctx, connect.NewRequest(&v1.ListAgentProfilesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		driverConfig, _ := structpb.NewStruct(map[string]any{"emulated_tui": true})
		_, err = f.clients.Profiles.PutAgentProfile(ctx, connect.NewRequest(&v1.PutAgentProfileRequest{Profile: &v1.AgentProfile{Name: "emulated", Driver: "pi", Config: driverConfig}, ExpectedRevision: profiles.Msg.Revision}))
		if err != nil {
			t.Fatal(err)
		}
		started, err := f.clients.Lifecycle.CreateAndStartItem(ctx, connect.NewRequest(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repository, Title: "Isolated fake TUI", AgentProfile: "emulated"}, Start: &v1.StartOptions{Mode: v1.AgentMode_AGENT_MODE_TUI, SendNothing: true}}))
		if err != nil {
			t.Fatal(err)
		}
		wait(started.Msg.Job)
		item := started.Msg.Item.Id
		current := f.idle(item)
		if current.Terminal == nil || current.Terminal.Session == "" || current.Terminal.TmuxSocket != socket || current.Mode != v1.AgentMode_AGENT_MODE_TUI {
			t.Fatalf("TUI target: %v", current)
		}
		target := current.Terminal
		t.Cleanup(func() {
			command := exec.Command("tmux", "-S", socket, "kill-session", "-t", target.Session)
			command.Env = f.env
			_ = command.Run()
		})
		command := exec.Command("tmux", "-S", socket, "display-message", "-p", "-t", target.Session+":"+target.Window, "#{session_name}:#{pane_id}")
		command.Env = f.env
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("reported pane cannot be inspected: %v %s", err, out)
		}
		// The command line drives a keystroke TUI as it is: taking control
		// would neither replace the agent nor need confirmation.
		cliRequest := connect.NewRequest(&v1.GetControlRequest{ItemId: item, ClientId: "cli"})
		cliRequest.Header().Set("Wi-Client-Kind", "cli")
		cliPlan, err := f.clients.Control.GetControl(ctx, cliRequest)
		if err != nil {
			t.Fatal(err)
		}
		if p := cliPlan.Msg.Control.GetPlan(); p.GetRestartsIn() != v1.AgentMode_AGENT_MODE_UNSPECIFIED || p.GetNeedsConfirmation() || p.GetUnavailable() != "" {
			t.Fatalf("command-line control plan for a keystroke TUI: %v", p)
		}
		plan, err := f.clients.Control.GetControl(ctx, connect.NewRequest(&v1.GetControlRequest{ItemId: item, ClientId: "cli"}))
		if err != nil {
			t.Fatal(err)
		}
		acquired, err := f.clients.Control.AcquireControl(ctx, connect.NewRequest(&v1.AcquireControlRequest{ItemId: item, ClientId: "cli", StartAgent: true, Confirm: true, PlanToken: plan.Msg.Control.Plan.PlanToken}))
		if err != nil || acquired.Msg.Job == nil {
			t.Fatalf("replacement should be a job: %v %v", acquired, err)
		}
		wait(acquired.Msg.Job)
		// A browser sends as soon as its takeover returns: the replacement
		// must accept input then, not only once observed idle.
		sent, err := f.clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: "right after takeover", Delivery: v1.Delivery_DELIVERY_PROMPT, ClientMessageId: "after-takeover"}))
		if err != nil {
			t.Fatalf("send right after takeover: %v", err)
		}
		if sent.Msg.Job != nil {
			wait(sent.Msg.Job)
		}
		replacement := f.idle(item)
		if replacement.Mode != v1.AgentMode_AGENT_MODE_HEADLESS || replacement.Generation <= current.Generation {
			t.Fatalf("replacement agent: %v", replacement)
		}
		takeover := connect.NewRequest(&v1.AcquireControlRequest{ItemId: item, ClientId: "other", StartAgent: true})
		takeover.Header().Set("Wi-Client-ID", "other")
		_, err = f.clients.Control.AcquireControl(ctx, takeover)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("unconfirmed takeover: %v", err)
		}
		otherPlan := connect.NewRequest(&v1.GetControlRequest{ItemId: item, ClientId: "other"})
		otherPlan.Header().Set("Wi-Client-ID", "other")
		view, err := f.clients.Control.GetControl(ctx, otherPlan)
		if err != nil {
			t.Fatal(err)
		}
		takeover.Msg.PlanToken = view.Msg.Control.Plan.PlanToken
		takeover.Msg.Confirm = true
		taken, err := f.clients.Control.AcquireControl(ctx, takeover)
		if err != nil || taken.Msg.Control.Lease.ClientId != "other" {
			t.Fatalf("confirmed takeover: %v %v", taken, err)
		}
		// A browser sending to a detached TUI agent takes the vacant control
		// itself: the agent is replaced by a headless one that receives the
		// message.
		detached, err := f.clients.Lifecycle.CreateAndStartItem(ctx, connect.NewRequest(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repository, Title: "Detached fake TUI", AgentProfile: "emulated"}, Start: &v1.StartOptions{Mode: v1.AgentMode_AGENT_MODE_TUI, SendNothing: true}}))
		if err != nil {
			t.Fatal(err)
		}
		wait(detached.Msg.Job)
		tui := f.idle(detached.Msg.Item.Id)
		browser := connect.NewRequest(&v1.SendMessageRequest{ItemId: detached.Msg.Item.Id, Text: "from a browser", Delivery: v1.Delivery_DELIVERY_PROMPT, ClientMessageId: "browser-1"})
		browser.Header().Set("Wi-Client-ID", "browser")
		browser.Header().Set("Wi-Client-Kind", "web")
		delivered, err := f.clients.Agents.SendMessage(ctx, browser)
		if err != nil {
			t.Fatalf("browser send to a detached TUI agent: %v", err)
		}
		if delivered.Msg.Job != nil {
			wait(delivered.Msg.Job)
		}
		if replaced := f.idle(detached.Msg.Item.Id); !replaced.Online || replaced.Mode != v1.AgentMode_AGENT_MODE_HEADLESS || replaced.Generation <= tui.Generation {
			t.Fatalf("browser send left the agent: %v", replaced)
		}
		f.run("close", detached.Msg.Item.Id, "--force")
		f.run("close", item, "--force")
	})
}
