package cli_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func TestEditTitleEditsMetadataInEveryLifecycleState(t *testing.T) {
	for _, state := range []string{model.StateBacklog, model.StateActive, model.StateClosed} {
		t.Run(state, func(t *testing.T) {
			stdout, stderr, a := configuredApp(t)
			ctx := context.Background()
			created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Untitled: true, CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			m := created.Manifest
			m.State = state
			if state == model.StateClosed {
				m.Slug = ""
			}
			if err := a.Store.SaveManifest(ctx, m); err != nil {
				t.Fatal(err)
			}
			before, err := a.Store.LoadManifest(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			eventsBefore, err := a.Store.ReadEvents(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			// No driver is needed, even if configuration for future launches is broken.
			a.ConversationBackends = nil
			cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ID": m.ID}, App: a}
			for _, changed := range []bool{true, false} {
				stdout.Reset()
				stderr.Reset()
				args := []string{"edit", "--title", "  Clarified objective  ", "--json"}
				if state == model.StateClosed {
					args = append([]string{"edit", "--item", m.ID}, args[1:]...)
				}
				code := cli.Run(ctx, args, cfg)
				if code != cli.ExitOK {
					t.Fatalf("code=%d stderr=%s", code, stderr)
				}
				var result struct {
					WorkItemID string         `json:"work_item_id"`
					Title      string         `json:"title"`
					Changed    bool           `json:"changed"`
					Manifest   model.Manifest `json:"manifest"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.WorkItemID != m.ID || result.Title != "Clarified objective" || result.Changed != changed {
					t.Fatalf("result=%+v", result)
				}
				after := result.Manifest
				after.Title = before.Title
				after.UpdatedAt = before.UpdatedAt
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("title edit changed other metadata: before=%+v after=%+v", before, after)
				}
			}
			eventsAfter, err := a.Store.ReadEvents(m.ID)
			if err != nil || len(eventsAfter) != len(eventsBefore)+1 || eventsAfter[len(eventsAfter)-1].Type != "work_item.edited" {
				t.Fatalf("events=%+v err=%v", eventsAfter, err)
			}
			stdout.Reset()
			if code := cli.Run(ctx, []string{"edit", "--item", m.ID, "--title", "Text output"}, cfg); code != cli.ExitOK || !strings.Contains(stdout.String(), "edited ") {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
		})
	}
}

func TestEditTitleRejectsInvalidInputWithoutMutation(t *testing.T) {
	// Cases are named, not titled by their arguments: a test's temporary
	// directory takes its name, and an overlong title exceeds the file name
	// limit of Linux file systems.
	for _, test := range []struct {
		name string
		args []string
	}{
		{"missing", []string{"edit", "--title"}},
		{"several words unquoted", []string{"edit", "--title", "one", "two", "three"}},
		{"blank", []string{"edit", "--title", " "}},
		{"line break", []string{"edit", "--title", "line\nbreak"}},
		{"control character", []string{"edit", "--title", "control\x1b"}},
		{"too long", []string{"edit", "--title", strings.Repeat("界", 171)}},
		{"invalid UTF-8", []string{"edit", "--title", string([]byte{0xff})}},
	} {
		args := test.args
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, a := configuredApp(t)
			created, err := a.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Original", CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ID": created.Manifest.ID}, App: a}
			if code := cli.Run(context.Background(), args, cfg); code != cli.ExitUsage {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			after, err := a.Store.LoadManifest(created.Manifest.ID)
			if err != nil || !reflect.DeepEqual(after, created.Manifest) {
				t.Fatalf("rejected edit changed item: %+v %v", after, err)
			}
		})
	}
}

func TestEditTitleRejectsMissingItemAndAgentEndpoint(t *testing.T) {
	stdout, stderr, a := configuredApp(t)
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: a}
	if code := cli.Run(context.Background(), []string{"edit", "--item", "missing", "--title", "New title"}, cfg); code != cli.ExitNotFound {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	cfg.ControlAccess = coordinator.AccessAgent
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"edit", "--title", "New title"}, cfg); code != cli.ExitError || !strings.Contains(stderr.String(), "does not allow item edits") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}
