package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/tmux"
)

func TestNewUntitledCreatesEditableBacklogRecord(t *testing.T) {
	stdout, stderr, a := configuredApp(t)
	cwd := t.TempDir()
	description := "Objective to be clarified.\n"
	if err := os.WriteFile(filepath.Join(cwd, "task.md"), []byte(description), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: cwd, Env: map[string]string{}, App: a}
	code := cli.Run(context.Background(), []string{"new", "--untitled", "--desc-file", "task.md", "--label", "triage", "--json"}, cfg)
	if code != cli.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var result struct {
		Manifest model.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	m := result.Manifest
	if m.Title != model.UntitledTitle || m.Slug != model.IDDerivedSlug(m.ID) || m.Checkout.Specification.Branch != model.ItemBranchName(m.Slug, m.ID) || m.State != model.StateBacklog || m.Checkout.Claim != nil || m.PrimaryConversation != nil {
		t.Fatalf("manifest=%+v", m)
	}
	shown, err := a.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: m.ID})
	if err != nil || shown.Description != description {
		t.Fatalf("shown=%+v err=%v", shown, err)
	}
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil || runtime != nil {
		t.Fatalf("runtime=%+v err=%v", runtime, err)
	}
	m.Title = "Clarified objective"
	if err := a.Store.SaveManifest(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	changed, err := a.Store.LoadManifest(m.ID)
	if err != nil || changed.Title != m.Title || changed.Slug != result.Manifest.Slug || changed.Checkout.Specification.Branch != result.Manifest.Checkout.Specification.Branch {
		t.Fatalf("title update=%+v err=%v", changed, err)
	}
	// Restore follows the existing current-title policy, not a durable untitled mode.
	for _, state := range []string{model.StateArchived, model.StateBacklog} {
		stdout.Reset()
		stderr.Reset()
		if code := cli.Run(context.Background(), []string{"state", "set", state, "--item", m.ID}, cfg); code != cli.ExitOK {
			t.Fatalf("state %s code=%d stderr=%s", state, code, stderr)
		}
	}
	restored, err := a.Store.LoadManifest(m.ID)
	if err != nil || restored.Slug != "clarified-objective" || restored.Checkout.Specification.Branch != m.Checkout.Specification.Branch {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
}

func TestNewUntitledFlagRules(t *testing.T) {
	for _, args := range [][]string{
		{"new", "--untitled", "--title", "Named"},
		{"new", "--untitled", "--title", ""},
		{"new", "--untitled=false"},
		{"new", "--untitled", "extra"},
		{"new", "--untitled", "--prompt", "A question", "--start=false"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, a := configuredApp(t)
			if code := cli.Run(context.Background(), args, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: a}); code != cli.ExitUsage {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			items, errs := a.Store.ListManifests()
			if len(items) != 0 || len(errs) != 0 {
				t.Fatalf("invalid flags created items: %v %v", items, errs)
			}
		})
	}
}

func TestNewUntitledAllowsExplicitSlug(t *testing.T) {
	stdout, stderr, a := configuredApp(t)
	if code := cli.Run(context.Background(), []string{"new", "--untitled", "--slug", "capture"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: a}); code != cli.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	items, errs := a.Store.ListManifests()
	if len(items) != 1 || len(errs) != 0 || items[0].Title != model.UntitledTitle || items[0].Slug != "capture" {
		t.Fatalf("items=%+v errors=%v", items, errs)
	}
}

type untitledStartupFailureTmux struct{ fakeTmux }

func (*untitledStartupFailureTmux) EnsureSession(context.Context, tmux.SessionSpec) (bool, error) {
	return false, os.ErrPermission
}

func TestNewUntitledStartPreservesPartialOutcome(t *testing.T) {
	for _, mode := range []string{"tui", "headless"} {
		t.Run(mode, func(t *testing.T) {
			stdout, stderr, a := configuredApp(t)
			a.SelfPath = "/usr/local/bin/wi"
			// TUI composition fails at tmux launch; headless fails at runtime launch.
			a.Tmux = &untitledStartupFailureTmux{}
			a.AgentRuntimeLauncher = failedStartupLauncher{}
			code := cli.Run(context.Background(), []string{"new", "--untitled", "--start", "--agent-mode", mode}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: a})
			if code != cli.ExitError || !strings.Contains(stderr.String(), "created work item") {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			items, errs := a.Store.ListManifests()
			if len(items) != 1 || len(errs) != 0 || items[0].State != model.StateWorking || items[0].Title != model.UntitledTitle || items[0].Slug != model.IDDerivedSlug(items[0].ID) {
				t.Fatalf("items=%+v errors=%v", items, errs)
			}
		})
	}
}
