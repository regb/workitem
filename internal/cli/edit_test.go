package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/model"
)

func TestEditCombinedFieldsAndNoOp(t *testing.T) {
	stdout, stderr, a := configuredApp(t)
	created, err := a.NewWorkItem(context.Background(), app.NewWorkItemOptions{Untitled: true, Labels: []string{"blocked"}, CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "description.md")
	if err := os.WriteFile(path, []byte("Full objective\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: dir, Env: map[string]string{"WI_ID": created.Manifest.ID}, App: a}
	args := []string{"edit", "--title", "Clarified objective", "--desc-file", path, "--label", "BACKEND", "--label", "urgent", "--remove-label", "blocked", "--deep", "--json"}
	for _, changed := range []bool{true, false} {
		stdout.Reset()
		stderr.Reset()
		code := cli.Run(context.Background(), args, cfg)
		if code != cli.ExitOK {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
		var result struct {
			Changed            bool           `json:"changed"`
			Manifest           model.Manifest `json:"manifest"`
			DescriptionChanged bool           `json:"description_changed"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Changed != changed || result.DescriptionChanged != changed || result.Manifest.Title != "Clarified objective" || !result.Manifest.DeepWork || !reflect.DeepEqual(result.Manifest.Labels, []string{"backend", "urgent"}) || result.Manifest.Slug != created.Manifest.Slug || result.Manifest.Checkout.Specification.Branch != created.Manifest.Checkout.Specification.Branch {
			t.Fatalf("result=%+v", result)
		}
	}
	shown, err := a.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID})
	if err != nil || shown.Description != "Full objective\n" {
		t.Fatalf("shown=%+v err=%v", shown, err)
	}
	// Empty file is an explicit clearing operation, not an omitted description.
	os.WriteFile(path, nil, 0600)
	stdout.Reset()
	if code := cli.Run(context.Background(), []string{"edit", "--desc-file", path, "--no-deep"}, cfg); code != cli.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	shown, err = a.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID})
	if err != nil || shown.Description != "" || shown.Manifest.DeepWork {
		t.Fatalf("shown=%+v err=%v", shown, err)
	}
}

func TestEditDescriptionLimitThroughDaemonTransport(t *testing.T) {
	stdout, stderr, a := configuredApp(t)
	created, err := a.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Large description", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "large.md")
	// NUL is valid UTF-8 and forces six-byte JSON escaping per source byte.
	description := strings.Repeat("\x00", 4<<20)
	if err := os.WriteFile(path, []byte(description), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ID": created.Manifest.ID}, App: a}
	if code := cli.Run(context.Background(), []string{"edit", "--desc-file", path, "--json"}, cfg); code != cli.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	shown, err := a.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID})
	if err != nil || shown.Description != description {
		t.Fatalf("description bytes=%d err=%v", len(shown.Description), err)
	}
}

func TestEditRejectsCombinedInvalidPatchWithoutPartialChange(t *testing.T) {
	for _, flags := range [][]string{{"--label", "!invalid"}, {"--label", "same", "--remove-label", "SAME"}, {"--deep", "--no-deep"}, {"--state", "working"}, {"--slug", "different"}, {"--desc-file", "missing.md"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			stdout, stderr, a := configuredApp(t)
			created, err := a.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Original", CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ID": created.Manifest.ID}, App: a}
			args := append([]string{"edit", "--title", "Do not apply"}, flags...)
			if code := cli.Run(context.Background(), args, cfg); code == cli.ExitOK {
				t.Fatal("invalid edit succeeded")
			}
			after, err := a.Store.LoadManifest(created.Manifest.ID)
			if err != nil || !reflect.DeepEqual(after, created.Manifest) {
				t.Fatalf("partial edit=%+v err=%v", after, err)
			}
		})
	}
}

type editMutationReader struct {
	mutate func()
	called bool
}

func (r *editMutationReader) Read([]byte) (int, error) {
	if !r.called {
		r.called = true
		r.mutate()
	}
	return 0, io.EOF
}

func TestEditInteractiveDraft(t *testing.T) {
	for _, mode := range []string{"change", "unchanged", "failure", "invalid", "concurrent", "metadata concurrent"} {
		t.Run(mode, func(t *testing.T) {
			stdout, stderr, a := configuredApp(t)
			created, err := a.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Original", Description: "original body\n", CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			editor := filepath.Join(dir, "editor with spaces")
			pathLog := filepath.Join(dir, "draft-path")
			script := "#!/bin/sh\nset -eu\nprintf '%s' \"$1\" > '" + pathLog + "'\n"
			switch mode {
			case "change":
				script += "printf 'Title: Edited title\\n\\nEdited body\\n' > \"$1\"\n"
			case "failure":
				script += "exit 9\n"
			case "invalid":
				script += "printf 'invalid header' > \"$1\"\n"
			case "metadata concurrent":
				script += "cat > /dev/null\nprintf 'Title: Must not apply\\n\\nEdited body\\n' > \"$1\"\n"
			case "concurrent":
				script += "printf 'external write' > '" + filepath.Join(a.Store.ItemDir(created.Manifest.ID), model.DescriptionFilename) + "'\nprintf 'Title: Must not apply\\n\\nEdited body\\n' > \"$1\"\n"
			}
			if err := os.WriteFile(editor, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: dir, Env: map[string]string{"WI_ID": created.Manifest.ID, "EDITOR": "false", "VISUAL": "'" + editor + "'"}, App: a}
			if mode == "metadata concurrent" {
				cfg.Stdin = &editMutationReader{mutate: func() {
					m := created.Manifest
					m.Labels = []string{"concurrent"}
					if err := a.Store.SaveManifest(context.Background(), m); err != nil {
						panic(err)
					}
				}}
			}
			code := cli.Run(context.Background(), []string{"edit", "--json"}, cfg)
			draftBytes, err := os.ReadFile(pathLog)
			if err != nil {
				t.Fatal(err)
			}
			draft := string(draftBytes)
			t.Cleanup(func() { os.RemoveAll(filepath.Dir(draft)) })
			after, err := a.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "change" || mode == "unchanged" {
				if code != cli.ExitOK {
					t.Fatalf("code=%d stderr=%s", code, stderr)
				}
				if mode == "change" && (after.Manifest.Title != "Edited title" || after.Description != "Edited body\n") {
					t.Fatalf("after=%+v", after)
				}
				if _, err := os.Stat(draft); !os.IsNotExist(err) {
					t.Fatal("successful draft retained")
				}
			} else {
				if code == cli.ExitOK || !strings.Contains(stderr.String(), "draft retained") {
					t.Fatalf("code=%d stderr=%s", code, stderr)
				}
				if after.Manifest.Title != "Original" {
					t.Fatal("failed editor changed title")
				}
				info, err := os.Stat(draft)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("failed draft missing or not private")
				}
				if mode == "concurrent" && after.Description != "external write" {
					t.Fatal("concurrent description overwritten")
				}
			}
			if mode == "metadata concurrent" && !reflect.DeepEqual(after.Manifest.Labels, []string{"concurrent"}) {
				t.Fatal("concurrent metadata overwritten")
			}
			if after.Manifest.Slug != created.Manifest.Slug || after.Manifest.Checkout.Specification.Branch != created.Manifest.Checkout.Specification.Branch || after.Manifest.PrimaryConversation != nil || after.Manifest.Checkout.Claim != nil {
				t.Fatal("edit affected resource intent")
			}
		})
	}
}
