package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	viewapp "github.com/regb/workitem/internal/app/view"
)

func TestSearchFindsItemsByDescriptionAndNotes(t *testing.T) {
	cfg, _, _, _ := remoteCLI(t)
	ctx := context.Background()
	described, _ := newFollowItem(t, cfg)
	description := filepath.Join(t.TempDir(), "description.md")
	if err := os.WriteFile(description, []byte("The session cache\nkeeps stale tokens."), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := Run(ctx, []string{"edit", "--item", described.ID, "--desc-file", description}, cfg); code != ExitOK {
		t.Fatalf("edit code=%d stderr=%v", code, cfg.Stderr)
	}
	noted, _ := newFollowItem(t, cfg)
	if code := Run(ctx, []string{"note", "--item", noted.ID, "root cause is a shared fixture"}, cfg); code != ExitOK {
		t.Fatalf("note code=%d stderr=%v", code, cfg.Stderr)
	}
	search := func(args ...string) viewapp.SearchResult {
		t.Helper()
		var out bytes.Buffer
		cfg.Stdout = &out
		if code := Run(ctx, append([]string{"--json", "search"}, args...), cfg); code != ExitOK {
			t.Fatalf("search %v code=%d stderr=%v", args, code, cfg.Stderr)
		}
		var result viewapp.SearchResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := search(`"session cache keeps"`); len(got.Items) != 1 || got.Items[0].ID != described.ID || got.Items[0].Field != viewapp.SearchFieldDescription {
		t.Fatalf("description search: %+v", got.Items)
	}
	if got := search("shared", "FIXTURE"); len(got.Items) != 1 || got.Items[0].ID != noted.ID || !strings.Contains(got.Items[0].Excerpt, "shared fixture") {
		t.Fatalf("notes search: %+v", got.Items)
	}
	if got := search("--state", "closed", "fixture"); len(got.Items) != 0 {
		t.Fatalf("state filter: %+v", got.Items)
	}
	var out bytes.Buffer
	cfg.Stdout = &out
	if code := Run(ctx, []string{"search", "fixture"}, cfg); code != ExitOK || !strings.Contains(out.String(), "notes") || !strings.Contains(out.String(), "root cause is a shared fixture") {
		t.Fatalf("text output code=%d: %s", code, out.String())
	}
	if code := Run(ctx, []string{"search"}, cfg); code == ExitOK {
		t.Fatal("empty query accepted")
	}
}
