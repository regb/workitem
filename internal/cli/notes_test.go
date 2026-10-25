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
	"github.com/regb/workitem/internal/itemnotes"
)

func TestNotesAndAttachmentsFormTheItemLog(t *testing.T) {
	stdout, stderr, a := configuredApp(t)
	ctx := context.Background()
	created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Research", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: cwd, Env: map[string]string{"WI_ID": created.Manifest.ID}, App: a}
	run := func(args ...string) string {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := cli.Run(ctx, args, cfg); code != cli.ExitOK {
			t.Fatalf("%v: code=%d stderr=%s", args, code, stderr)
		}
		return stdout.String()
	}
	run("note", "Dropped", "the cache approach")
	os.WriteFile(filepath.Join(cwd, "REPORT.md"), []byte("# First\n"), 0o600)
	run("attach", "REPORT.md")
	os.WriteFile(filepath.Join(cwd, "REPORT.md"), []byte("# Second\n"), 0o600)
	run("attach", "-m", "Report revised", "REPORT.md")

	var log struct{ Notes []itemnotes.Note }
	if err := json.Unmarshal([]byte(run("--json", "notes")), &log); err != nil {
		t.Fatal(err)
	}
	if len(log.Notes) != 3 || log.Notes[0].Text != "Dropped the cache approach" || log.Notes[1].Text != "Attached REPORT.md" || log.Notes[2].Text != "Report revised" {
		t.Fatalf("notes: %+v", log.Notes)
	}
	if text := run("notes"); !strings.Contains(text, "Dropped the cache approach") || !strings.Contains(text, "Report revised [REPORT.md]") {
		t.Fatalf("text notes:\n%s", text)
	}
	if text := run("attachments"); strings.Count(text, "REPORT.md") != 1 {
		t.Fatalf("attachments:\n%s", text)
	}
	if text := run("attachment", "cat", "REPORT.md"); text != "# Second\n" {
		t.Fatalf("current content %q", text)
	}
	if text := run("attachment", "cat", log.Notes[1].Attachment.SHA256); text != "# First\n" {
		t.Fatalf("earlier content %q", text)
	}
	stderr.Reset()
	if code := cli.Run(ctx, []string{"attachment", "cat", "MISSING.md"}, cfg); code != cli.ExitNotFound {
		t.Fatalf("missing attachment: code=%d stderr=%s", code, stderr)
	}
	for _, args := range [][]string{{"note"}, {"note", strings.Repeat("x", itemnotes.MaxNoteBytes+1)}, {"attach"}, {"attach", "--as", "../x", "REPORT.md"}} {
		if code := cli.Run(ctx, args, cfg); code != cli.ExitUsage {
			t.Fatalf("%v: code=%d", args[:1], code)
		}
	}
}
