package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/cli"
)

func TestRemovedCreationAndRelocationCommands(t *testing.T) {
	for _, args := range [][]string{
		{"title", "Rejected"},
		{"label", "backend"},
		{"deep"},
		{"start", "--new", "Rejected"},
		{"start", "--new", "--home", "Rejected"},
		{"start", "--home", "existing"},
		{"start", "--no-default-labels", "existing"},
		{"start", "--agent", "pi", "existing"},
		{"new", "--home", "--title", "Rejected"},
		{"workspace", "relocate", "--repository", "/other", "existing"},
		{"workspace", "release", "--force", "existing"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, application := configuredApp(t)
			ctx := context.Background()
			if _, err := application.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Existing", CWD: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			before, errs := application.Store.ListManifests()
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			beforeJSON, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
			if code := cli.Run(ctx, append([]string{"--json"}, args...), cfg); code != cli.ExitUsage {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if stderr.Len() == 0 || stdout.Len() != 0 {
				t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			after, errs := application.Store.ListManifests()
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			afterJSON, err := json.Marshal(after)
			if err != nil {
				t.Fatal(err)
			}
			if string(beforeJSON) != string(afterJSON) {
				t.Fatalf("rejected command changed items: before=%s after=%s", beforeJSON, afterJSON)
			}
		})
	}
}

func TestHelpDoesNotAdvertiseRemovedCommands(t *testing.T) {
	for _, name := range []string{"title", "label", "deep"} {
		stdout, stderr, application := configuredApp(t)
		cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
		if code := cli.Run(context.Background(), []string{"help", name}, cfg); code != cli.ExitUsage {
			t.Fatalf("removed help topic %s code=%d stdout=%s", name, code, stdout)
		}
	}
	for _, args := range [][]string{{"start", "--help"}, {"new", "--help"}, {"workspace", "--help"}, {"workspace", "release", "--help"}} {
		stdout, stderr, application := configuredApp(t)
		cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
		if code := cli.Run(context.Background(), args, cfg); code != cli.ExitOK {
			t.Fatalf("%v: code=%d stderr=%s", args, code, stderr.String())
		}
		removedOptions := []string{"--home", "--new", "relocate"}
		if len(args) == 3 && args[1] == "release" {
			removedOptions = append(removedOptions, "--force")
		}
		for _, removed := range removedOptions {
			if strings.Contains(stdout.String(), removed) {
				t.Fatalf("%v advertises %s: %s", args, removed, stdout.String())
			}
		}
	}
}
