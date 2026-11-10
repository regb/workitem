package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func waitClaudeJob(t *testing.T, ctx context.Context, f *binaryFixture, job *v1.Job) {
	t.Helper()
	if job == nil {
		return
	}
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

const claudeStartupTask = "Read this task\nKeep this second line"

func TestClaudeStartupDelivery(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	f := newBinaryFixture(t)
	backend := filepath.Join(f.root, "fake-claude")
	cmd := exec.Command("go", "build", "-o", backend, "./internal/apitest/claude")
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build backend: %v %s", err, out)
	}
	var added v1.AddRepositoryResponse
	f.json(&added, "repo", "add", "test", "--from", f.source, "--local")
	for _, scenario := range []struct{ reject, headless bool }{{}, {reject: true}, {headless: true}} {
		reject, headless := scenario.reject, scenario.headless
		name := fmt.Sprintf("copy-mode-%t-headless-%t", reject, headless)
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			root := filepath.Join(f.root, "claude-"+name)
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			wrapper := filepath.Join(root, "claude")
			if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexport WI_FAKE_CLAUDE_ROOT=%q\nexec %q \"$@\"\n", root, backend)), 0700); err != nil {
				t.Fatal(err)
			}
			profiles, err := f.clients.Profiles.ListAgentProfiles(ctx, connect.NewRequest(&v1.ListAgentProfilesRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			config, _ := structpb.NewStruct(map[string]any{"executable": wrapper})
			profile := "claude-" + name
			_, err = f.clients.Profiles.PutAgentProfile(ctx, connect.NewRequest(&v1.PutAgentProfileRequest{Profile: &v1.AgentProfile{Name: profile, Driver: "claude", Config: config}, ExpectedRevision: profiles.Msg.Revision}))
			if err != nil {
				t.Fatal(err)
			}
			mode := v1.AgentMode_AGENT_MODE_TUI
			if headless {
				mode = v1.AgentMode_AGENT_MODE_HEADLESS
			}
			request := connect.NewRequest(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: added.Repository.Id, Title: profile, AgentProfile: profile}, Start: &v1.StartOptions{Mode: mode, Prompt: claudeStartupTask, ClientMessageId: "initial"}})
			request.Header().Set("Wi-Client-Kind", "cli")
			request.Header().Set("Wi-Client-ID", "cli")
			started, err := f.clients.Lifecycle.CreateAndStartItem(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			item := started.Msg.Item.Id
			var target *v1.Terminal
			// Wait for queue acceptance before allowing the fake native SessionStart.
			for {
				events, err := f.clients.Items.ListItemEvents(ctx, connect.NewRequest(&v1.ListItemEventsRequest{ItemId: item, PageSize: 100}))
				if err != nil {
					t.Fatal(err)
				}
				queued := false
				for _, event := range events.Msg.Events {
					if strings.Contains(event.PayloadJson, `"delivery_state":"queued"`) || strings.Contains(event.PayloadJson, `"delivery_state":"submitted"`) {
						queued = true
					}
				}
				if queued {
					current, err := f.clients.Agents.GetAgent(ctx, connect.NewRequest(&v1.GetAgentRequest{ItemId: item}))
					if err != nil {
						t.Fatal(err)
					}
					target = current.Msg.Agent.Terminal
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(20 * time.Millisecond):
				}
			}
			if target == nil && !headless {
				t.Fatal("missing TUI")
			}
			tmux := func(args ...string) string {
				t.Helper()
				command := exec.CommandContext(ctx, "tmux", append([]string{"-S", target.TmuxSocket}, args...)...)
				command.Env = f.env
				out, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("tmux: %v %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			pane := ""
			if !headless {
				pane = tmux("display-message", "-p", "-t", target.Session+":"+target.Window, "#{pane_id}")
				meta := tmux("display-message", "-p", "-t", pane, "#{pane_dead}|#{pane_in_mode}|#{pane_input_off}")
				if meta != "0|0|1" {
					t.Fatalf("API controller did not fence keyboard input: %s", meta)
				}
			}
			job, err := f.clients.Jobs.GetJob(ctx, connect.NewRequest(&v1.GetJobRequest{JobId: started.Msg.Job.Id}))
			if err != nil {
				t.Fatal(err)
			}
			if job.Msg.Job.State == v1.JobState_JOB_STATE_SUCCEEDED {
				t.Fatal("startup job succeeded on queue acceptance")
			}
			if !headless {
				tmux("set-option", "-p", "-t", pane, "remain-on-exit", "on")
			}
			if reject {
				tmux("copy-mode", "-t", pane)
			}
			queuedRetry := connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: claudeStartupTask, ClientMessageId: "initial", Delivery: v1.Delivery_DELIVERY_PROMPT})
			queuedRetry.Header().Set("Wi-Client-Kind", "cli")
			queuedRetry.Header().Set("Wi-Client-ID", "cli")
			retryDone := make(chan error, 1)
			go func() { _, err := f.clients.Agents.SendMessage(ctx, queuedRetry); retryDone <- err }()
			select {
			case err := <-retryDone:
				t.Fatalf("queued retry prematurely finished: %v", err)
			case <-time.After(75 * time.Millisecond):
			}
			if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			for {
				job, err = f.clients.Jobs.GetJob(ctx, connect.NewRequest(&v1.GetJobRequest{JobId: started.Msg.Job.Id}))
				if err != nil {
					t.Fatal(err)
				}
				if job.Msg.Job.State == v1.JobState_JOB_STATE_SUCCEEDED || job.Msg.Job.State == v1.JobState_JOB_STATE_FAILED {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(20 * time.Millisecond):
				}
			}
			retryErr := <-retryDone
			if (retryErr != nil) != reject {
				t.Fatalf("queued retry outcome: %v", retryErr)
			}
			want := v1.JobState_JOB_STATE_SUCCEEDED
			if reject {
				want = v1.JobState_JOB_STATE_FAILED
			}
			if job.Msg.Job.State != want {
				screen := ""
				if !headless {
					screen = tmux("capture-pane", "-p", "-t", pane)
				}
				t.Fatalf("job: %v\nscreen: %s", job.Msg.Job, screen)
			}
			retry := connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: claudeStartupTask, ClientMessageId: "initial", Delivery: v1.Delivery_DELIVERY_PROMPT})
			retry.Header().Set("Wi-Client-Kind", "cli")
			retry.Header().Set("Wi-Client-ID", "cli")
			_, err = f.clients.Agents.SendMessage(ctx, retry)
			if reject {
				if err == nil || !strings.Contains(err.Error(), "not sent") {
					t.Fatalf("retry concealed rejection: %v", err)
				}
				tmux("send-keys", "-X", "-t", pane, "cancel")
				// Restart only this fixture's blocked runtime, then explicitly resend
				// the known-unsent task under a new identity.
				_, err = f.clients.Agents.StopAgent(ctx, connect.NewRequest(&v1.StopAgentRequest{ItemId: item, Force: true, ConfirmAttachedTerminal: true}))
				if err != nil {
					t.Fatal(err)
				}
				restarted, err := f.clients.Agents.StartAgent(ctx, connect.NewRequest(&v1.StartAgentRequest{ItemId: item, Mode: v1.AgentMode_AGENT_MODE_TUI}))
				if err != nil {
					t.Fatal(err)
				}
				waitClaudeJob(t, ctx, f, restarted.Msg.Job)
				f.idle(item)
				retry.Msg.ClientMessageId = "explicit-recovery"
				recovered, err := f.clients.Agents.SendMessage(ctx, retry)
				if err != nil {
					t.Fatalf("known not_sent recovery: %v", err)
				}
				waitClaudeJob(t, ctx, f, recovered.Msg.Job)
				pane = tmux("display-message", "-p", "-t", target.Session+":"+target.Window, "#{pane_id}")
			} else if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, "prompts"))
			if err != nil {
				t.Fatal(err)
			}
			var prompt string
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &prompt); err != nil || prompt != claudeStartupTask {
				t.Fatalf("duplicate or altered prompts: %s %v", data, err)
			}
			if !headless {
				if got := tmux("display-message", "-p", "-t", pane, "#{pane_input_off}"); got != "1" {
					t.Fatalf("structured delivery enabled terminal keyboard input: %s", got)
				}
			}
		})
	}
}
