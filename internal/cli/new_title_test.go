package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/cli"
)

func TestNewRequiresExplicitTitle(t *testing.T) {
	for _, args := range [][]string{
		{"new"}, {"new", "Old positional title"}, {"new", "--title", "   "},
		{"new", "--slug", "slug-only"}, {"new", "--desc-file", "missing.md"},
		{"new", "--start", "--prompt", "A question"}, {"new", "--title", "Explicit", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, application := configuredApp(t)
			code := cli.Run(context.Background(), args, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
			if code != cli.ExitUsage || !strings.Contains(stderr.String(), "--title") {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			items, errs := application.Store.ListManifests()
			if len(items) != 0 || len(errs) != 0 {
				t.Fatalf("items=%v errors=%v", items, errs)
			}
		})
	}
}

func TestNewTitleDerivesSlugWithFlagsAfterTitle(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	code := cli.Run(context.Background(), []string{"new", "--title", "  Fix token refresh  ", "--label", "auth"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("code=%d error=%s", code, stderr)
	}
	items, errs := application.Store.ListManifests()
	if len(items) != 1 || len(errs) != 0 || items[0].Title != "Fix token refresh" || items[0].Slug != "fix-token-refresh" {
		t.Fatalf("items=%+v errors=%v", items, errs)
	}
}

func TestNewRecordsTheItemsModelAndReasoning(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	if code := cli.Run(context.Background(), []string{"new", "--title", "Bad level", "--reasoning", "extreme"}, cfg); code == cli.ExitOK || !strings.Contains(stderr.String(), "invalid reasoning level") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if items, _ := application.Store.ListManifests(); len(items) != 0 {
		t.Fatalf("invalid level created %+v", items)
	}
	if code := cli.Run(context.Background(), []string{"new", "--title", "Plan", "--model", "opus", "--reasoning", "xhigh"}, cfg); code != cli.ExitOK {
		t.Fatalf("code=%d error=%s", code, stderr)
	}
	items, errs := application.Store.ListManifests()
	if len(items) != 1 || len(errs) != 0 || items[0].AgentSettings["model"] != "opus" || items[0].AgentSettings["reasoning"] != "xhigh" {
		t.Fatalf("items=%+v errors=%v", items, errs)
	}
}
