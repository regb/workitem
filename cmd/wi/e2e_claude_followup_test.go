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
	"github.com/regb/workitem/internal/apitest"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestClaudeFollowUpAcknowledgesQueueBeforeNativeSubmission(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	f := newBinaryFixture(t)
	backend := apitest.Command(t, "./internal/apitest/claude")
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	for _, mode := range []v1.AgentMode{v1.AgentMode_AGENT_MODE_HEADLESS, v1.AgentMode_AGENT_MODE_TUI} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer cancel()
			root := filepath.Join(f.root, mode.String())
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"release", "hold-turn"} {
				if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
					t.Fatal(err)
				}
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
			profile := "followup-" + strings.ReplaceAll(strings.ToLower(mode.String()), "_", "-")
			_, err = f.clients.Profiles.PutAgentProfile(ctx, connect.NewRequest(&v1.PutAgentProfileRequest{Profile: &v1.AgentProfile{Name: profile, Driver: "claude", Config: config}, ExpectedRevision: profiles.Msg.Revision}))
			if err != nil {
				t.Fatal(err)
			}
			initial := connect.NewRequest(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repo.Repository.Id, Title: profile, AgentProfile: profile}, Start: &v1.StartOptions{Mode: mode, Prompt: "first turn", ClientMessageId: "first"}})
			initial.Header().Set("Wi-Client-Kind", "cli")
			initial.Header().Set("Wi-Client-ID", "cli")
			started, err := f.clients.Lifecycle.CreateAndStartItem(ctx, initial)
			if err != nil {
				t.Fatal(err)
			}
			item := started.Msg.Item.Id
			waitClaudeJob(t, ctx, f, started.Msg.Job)
			send := func() {
				t.Helper()
				short, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				request := connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: "follow up\nsecond line", ClientMessageId: "later", Delivery: v1.Delivery_DELIVERY_FOLLOW_UP})
				request.Header().Set("Wi-Client-Kind", "cli")
				request.Header().Set("Wi-Client-ID", "cli")
				result, err := f.clients.Agents.SendMessage(short, request)
				if err != nil {
					t.Fatalf("queued follow-up must acknowledge without native submission: %v", err)
				}
				if result.Msg.Job != nil {
					waitClaudeJob(t, ctx, f, result.Msg.Job)
				}
			}
			send()
			send() // Same identity while still queued must not add another entry.
			// Keep the turn active beyond the old 30-second API confirmation timeout.
			// TUI shares the API path; the headless case exercises the full wall clock.
			if mode == v1.AgentMode_AGENT_MODE_HEADLESS {
				select {
				case <-time.After(31 * time.Second):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				send()
			}
			events, err := f.clients.Items.ListItemEvents(ctx, connect.NewRequest(&v1.ListItemEventsRequest{ItemId: item, PageSize: 200}))
			if err != nil {
				t.Fatal(err)
			}
			queued := false
			for _, event := range events.Msg.Events {
				var facts struct {
					RequestID string `json:"request_id"`
					State     string `json:"delivery_state"`
				}
				if err := json.Unmarshal([]byte(event.PayloadJson), &facts); err != nil {
					t.Fatal(err)
				}
				if facts.RequestID != "later" {
					continue
				}
				if facts.State == "confirmed" {
					t.Fatal("queued follow-up was falsely native-confirmed")
				}
				if facts.State == "queued" {
					queued = true
				}
			}
			if !queued {
				t.Fatal("missing queued follow-up fact")
			}
			readPrompts := func() []string {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(root, "prompts"))
				if err != nil {
					t.Fatal(err)
				}
				var result []string
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					var text string
					if err := json.Unmarshal([]byte(line), &text); err != nil {
						t.Fatal(err)
					}
					result = append(result, text)
				}
				return result
			}
			if got := readPrompts(); len(got) != 1 || got[0] != "first turn" {
				t.Fatalf("follow-up disturbed active native turn: %q", got)
			}
			if err := os.WriteFile(filepath.Join(root, "finish-turn"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			for {
				events, err = f.clients.Items.ListItemEvents(ctx, connect.NewRequest(&v1.ListItemEventsRequest{ItemId: item, PageSize: 200}))
				if err != nil {
					t.Fatal(err)
				}
				confirmed := false
				for _, event := range events.Msg.Events {
					var facts struct {
						RequestID string `json:"request_id"`
						State     string `json:"delivery_state"`
					}
					if err := json.Unmarshal([]byte(event.PayloadJson), &facts); err != nil {
						t.Fatal(err)
					}
					if facts.RequestID == "later" && facts.State == "confirmed" {
						confirmed = true
					}
				}
				if confirmed {
					break
				}
				select {
				case <-time.After(20 * time.Millisecond):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			send() // A retry after confirmation is still the same native prompt.
			if got := readPrompts(); len(got) != 2 || got[1] != "follow up\nsecond line" {
				t.Fatalf("duplicated or altered follow-up: %q", got)
			}
		})
	}
}
