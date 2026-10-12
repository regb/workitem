package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/model"
)

type invalidConfigBackend struct{ fakeConversationBackend }

func (invalidConfigBackend) ValidateConfig(context.Context, string, json.RawMessage) error {
	return fmt.Errorf("required driver setting is missing")
}

func TestCLIDriverPreflightBeforeCreateAndStart(t *testing.T) {
	stdout, stderr, a := configuredApp(t)
	a.ConversationBackends["pi"] = invalidConfigBackend{}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: a}
	for _, mode := range []string{"tui", "headless"} {
		stdout.Reset()
		stderr.Reset()
		code := cli.Run(context.Background(), []string{"new", "--title", "Task", "--start", "--agent-mode", mode}, cfg)
		if code != cli.ExitError || !strings.Contains(stderr.String(), "required driver setting") || strings.Contains(stderr.String(), "created work item") {
			t.Fatalf("code=%d error=%s", code, stderr)
		}
		items, errs := a.Store.ListManifests()
		if len(items) != 0 || len(errs) != 0 {
			t.Fatalf("failed preflight created items: %v %v", items, errs)
		}
	}
	// Capture remains driver-independent; validation is required when executing.
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"new", "--title", "Task"}, cfg); code != 0 {
		t.Fatalf("backlog capture code=%d error=%s", code, stderr)
	}
	items, errs := a.Store.ListManifests()
	if len(items) != 1 || len(errs) != 0 {
		t.Fatalf("items=%v errors=%v", items, errs)
	}
	for _, mode := range []string{"tui", "headless"} {
		stdout.Reset()
		stderr.Reset()
		code := cli.Run(context.Background(), []string{"start", "--agent-mode", mode, items[0].Slug}, cfg)
		if code != cli.ExitError || !strings.Contains(stderr.String(), "required driver setting") {
			t.Fatalf("code=%d error=%s", code, stderr)
		}
		after, err := a.Store.LoadManifest(items[0].ID)
		if err != nil || after.State != model.StateBacklog || after.Checkout.Claim != nil || after.PrimaryConversation != nil {
			t.Fatalf("start changed item: %+v %v", after, err)
		}
	}
}
