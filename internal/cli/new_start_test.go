package cli_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

type failedStartupLauncher struct{}

func (failedStartupLauncher) Start(agent.LaunchSpec) (int, error) {
	return 0, fmt.Errorf("test startup failure")
}

func TestNewStartKeepsDescriptionIndependentOnPartialFailure(t *testing.T) {
	for _, tt := range []struct{ name, description, prompt string }{
		{name: "default", description: "Acceptance criteria: regression covered.\n"},
		{name: "override", description: "Acceptance criteria: regression covered.\n", prompt: "Start with a failing test."},
		{name: "message without description", prompt: "Start with a failing test."},
		{name: "implicit start", prompt: "Start with a failing test."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, application := configuredApp(t)
			application.AgentConfig.StartupMessage = "Read wi show first."
			application.SelfPath = "/usr/local/bin/wi"
			application.AgentRuntimeLauncher = failedStartupLauncher{}
			cwd := t.TempDir()
			args := []string{"new", "--agent-mode", "headless"}
			if tt.name != "implicit start" {
				args = append(args, "--start")
			}
			if tt.description != "" {
				file := filepath.Join(cwd, "task.md")
				if err := os.WriteFile(file, []byte(tt.description), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--desc-file", file)
			}
			if tt.prompt != "" {
				args = append(args, "--prompt", tt.prompt)
			}
			args = append(args, "--title", "Regression")
			code := cli.Run(context.Background(), args, cli.Config{Stdout: stdout, Stderr: stderr, CWD: cwd, Env: map[string]string{}, App: application})
			if code != cli.ExitError || !strings.Contains(stderr.String(), "test startup failure") || !strings.Contains(stderr.String(), "created work item") {
				t.Fatalf("code=%d output=%s error=%s", code, stdout, stderr)
			}
			items, errs := application.Store.ListManifests()
			if len(errs) != 0 || len(items) != 1 || items[0].State != model.StateWorking {
				t.Fatalf("items=%+v errors=%v", items, errs)
			}
			shown, err := application.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: items[0].ID})
			if err != nil || shown.Description != tt.description {
				t.Fatalf("description=%q error=%v", shown.Description, err)
			}
		})
	}
}

func TestAgentEndpointRejectsImplicitPromptStartBeforeCreation(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	code := cli.Run(context.Background(), []string{"new", "--prompt", "Run this task", "--title", "Restricted"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application, ControlAccess: coordinator.AccessAgent})
	if code != cli.ExitError || !strings.Contains(stderr.String(), "--start is not allowed") {
		t.Fatalf("code=%d error=%s", code, stderr)
	}
	items, errs := application.Store.ListManifests()
	if len(items) != 0 || len(errs) != 0 {
		t.Fatalf("items=%v errors=%v", items, errs)
	}
}

func TestAgentEndpointRejectsNewStartBeforeCreation(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	code := cli.Run(context.Background(), []string{"new", "--start", "--title", "Restricted"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application, ControlAccess: coordinator.AccessAgent})
	if code != cli.ExitError || !strings.Contains(stderr.String(), "--start is not allowed") {
		t.Fatalf("code=%d error=%s", code, stderr)
	}
	items, errs := application.Store.ListManifests()
	if len(items) != 0 || len(errs) != 0 {
		t.Fatalf("items=%v errors=%v", items, errs)
	}
}
