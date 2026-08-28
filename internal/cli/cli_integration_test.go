package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

type fakeGit struct {
	info             gitpkg.RepositoryInfo
	firstDetectedDir string
	branches         map[string]bool
	worktrees        map[string]string
	detectedDir      string
}

func (f *fakeGit) DetectRepository(ctx context.Context, dir, revision string) (gitpkg.RepositoryInfo, error) {
	if f.firstDetectedDir == "" {
		f.firstDetectedDir = dir
	}
	f.detectedDir = dir
	return f.info, nil
}
func (f *fakeGit) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (f *fakeGit) RepositoryHome(context.Context, string) (model.RepositoryHomeInfo, error) {
	return model.RepositoryHomeInfo{Path: f.info.Repository.RootAtCreation, Branch: "main"}, nil
}
func (f *fakeGit) Head(ctx context.Context, dir string) (string, error) {
	return strings.Repeat("b", 40), nil
}
func (f *fakeGit) StatusPorcelain(ctx context.Context, dir string) (string, error) { return "", nil }
func (f *fakeGit) CurrentBranch(ctx context.Context, dir string) (string, error) {
	if f.worktrees != nil {
		if branch := f.worktrees[dir]; branch != "" {
			return branch, nil
		}
	}
	return "main", nil
}
func (f *fakeGit) BranchExists(ctx context.Context, repoRoot, branch string) (bool, error) {
	return f.branches[branch], nil
}
func (f *fakeGit) WorktreeAdd(ctx context.Context, opts gitpkg.WorktreeAddOptions) error {
	if err := os.MkdirAll(opts.Path, 0o700); err != nil {
		return err
	}
	if f.worktrees == nil {
		f.worktrees = map[string]string{}
	}
	if opts.NewBranch {
		f.branches[opts.Branch] = true
	}
	branch := opts.Branch
	if branch == "" {
		branch = opts.StartPoint
	}
	f.worktrees[opts.Path] = branch
	return nil
}
func (f *fakeGit) Switch(ctx context.Context, dir, branch, startPoint string, create bool) error {
	if create {
		f.branches[branch] = true
	}
	if f.worktrees == nil {
		f.worktrees = map[string]string{}
	}
	f.worktrees[dir] = branch
	return nil
}
func (f *fakeGit) WorktreeRemove(ctx context.Context, repoRoot, path string, force bool) error {
	return os.RemoveAll(path)
}
func (f *fakeGit) DeleteBranch(ctx context.Context, repoRoot, branch string, force bool) error {
	delete(f.branches, branch)
	return nil
}

type fakeTmux struct {
	sessions map[string]bool
	ensured  []tmuxpkg.SessionSpec
	launched []tmuxpkg.LaunchSpec
	killed   []string
}

func (f *fakeTmux) HasSession(ctx context.Context, name string) (bool, error) {
	if f.sessions != nil {
		return f.sessions[name], nil
	}
	return false, nil
}
func (f *fakeTmux) EnsureSession(ctx context.Context, spec tmuxpkg.SessionSpec) (bool, error) {
	f.ensured = append(f.ensured, spec)
	if f.sessions == nil {
		f.sessions = map[string]bool{}
	}
	f.sessions[spec.Name] = true
	return true, nil
}
func (f *fakeTmux) LaunchCommand(ctx context.Context, spec tmuxpkg.LaunchSpec) error {
	f.launched = append(f.launched, spec)
	return nil
}
func (f *fakeTmux) PaneInfo(ctx context.Context, target string) (tmuxpkg.PaneInfo, error) {
	path, session := "/tmp", "wi-test"
	if len(f.launched) > 0 {
		path = f.launched[len(f.launched)-1].CWD
		session = f.launched[len(f.launched)-1].SessionName
	}
	return tmuxpkg.PaneInfo{SessionName: session, WindowName: "agent", PaneID: "%1", PaneIndex: "0", PanePID: 123, Command: "wi", CurrentPath: path}, nil
}
func (f *fakeTmux) AttachOrSwitch(ctx context.Context, name string, inTmux bool) error { return nil }
func (f *fakeTmux) KillSession(ctx context.Context, name string) error {
	f.killed = append(f.killed, name)
	if f.sessions != nil {
		delete(f.sessions, name)
	}
	return nil
}
func (f *fakeTmux) KillSessionAsync(ctx context.Context, name string) error {
	f.killed = append(f.killed, name)
	if f.sessions != nil {
		delete(f.sessions, name)
	}
	return nil
}
func (f *fakeTmux) CurrentSession(ctx context.Context) (string, error) { return "", nil }
func (f *fakeTmux) SessionEnvironment(ctx context.Context, name string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (f *fakeTmux) GlobalEnvironment(ctx context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestGlobalAndCommandHelp(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "global", args: []string{"--help"}, want: []string{"Work:", "Metadata and history:", "Agent and checkout:", "Administration:", "Item selection:", "wi help [command [subcommand]]"}},
		{name: "list flag", args: []string{"list", "--help"}, want: []string{"wi list - List and filter work items", "WI_LIST_LABELS", "config list.labels < WI_LIST_LABELS < CLI --label", "!,+personal"}},
		{name: "new prompt", args: []string{"new", "--help"}, want: []string{"--prompt <text>", "--agent-mode <mode>", "normal tmux runtime", "does not switch or attach"}},
		{name: "start new", args: []string{"start", "--help"}, want: []string{"--new", "--home", "--no-default-labels", "minimal item from the supplied title"}},
		{name: "control help", args: []string{"help", "agent", "control"}, want: []string{"wi agent control", "agent control send", "without consulting derived busy/idle status"}},
		{name: "nested short flag", args: []string{"workspace", "ensure", "-h"}, want: []string{"wi workspace ensure", "does not create tmux or a Pi conversation"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, application := configuredApp(t)
			code := cli.Run(context.Background(), tc.args, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
			if code != cli.ExitOK {
				t.Fatalf("exit code = %d stderr=%s", code, stderr.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("help missing %q:\n%s", want, stdout.String())
				}
			}
		})
	}
}

func TestVersionDoesNotRequireApplicationState(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := cli.Run(context.Background(), []string{"version", "--json"}, cli.Config{Stdout: stdout, Stderr: stderr}); code != cli.ExitOK {
		t.Fatalf("version code=%d stderr=%s", code, stderr.String())
	}
	var got struct {
		Version   string `json:"version"`
		GoVersion string `json:"go_version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Version == "" || got.GoVersion == "" {
		t.Fatalf("version = %+v err=%v output=%s", got, err, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"version", "extra"}, cli.Config{Stdout: stdout, Stderr: stderr}); code != cli.ExitUsage {
		t.Fatalf("version extra code=%d stderr=%s", code, stderr.String())
	}
}

func TestUnknownFlagPrintsRichCommandUsage(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	code := cli.Run(context.Background(), []string{"list", "--not-a-flag"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitUsage {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "wi list - List and filter work items") || !strings.Contains(stderr.String(), "WI_LIST_LABELS") {
		t.Fatalf("expected rich list usage:\n%s", stderr.String())
	}
}

func TestNewJSONOutputStability(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	cwd := t.TempDir()
	descPath := filepath.Join(cwd, "desc.md")
	if err := os.WriteFile(descPath, []byte("Race fix"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := cli.Run(context.Background(), []string{"--json", "new", "--slug", "Refresh Race", "--desc-file", "desc.md", "--label", "auth", "Fix refresh-token race"}, cli.Config{
		Stdout: stdout,
		Stderr: stderr,
		CWD:    cwd,
		Env:    map[string]string{},
		App:    application,
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d stderr=%s", code, stderr.String())
	}
	var got struct {
		WorkItemID      string         `json:"work_item_id"`
		ChangedArtifact string         `json:"changed_artifact"`
		State           string         `json:"state"`
		ItemDir         string         `json:"item_dir"`
		Warnings        []string       `json:"warnings"`
		Manifest        model.Manifest `json:"manifest"`
		Checkout        model.Checkout `json:"checkout"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, stdout.String())
	}
	if got.WorkItemID != got.Manifest.ID || got.ChangedArtifact != "work_item" || got.State != model.StateBacklog {
		t.Fatalf("unexpected mutation json: %+v", got)
	}
	if got.Manifest.Slug != "refresh-race" || got.Manifest.State != model.StateBacklog || len(got.Manifest.Labels) != 1 || got.Manifest.Labels[0] != "auth" {
		t.Fatalf("manifest json missing fields: %+v", got.Manifest)
	}
	description, err := os.ReadFile(filepath.Join(got.ItemDir, model.DescriptionFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(description) != "Race fix" {
		t.Fatalf("description = %q", string(description))
	}
	if got.Manifest.Checkout.Present() || got.Checkout.Present() {
		t.Fatalf("checkout state = manifest %q result %q", got.Manifest.Checkout.Presence(), got.Checkout.Presence())
	}
	if got.Manifest.TerminalSessionName() == "" {
		t.Fatalf("terminal configuration missing from manifest json")
	}
}

func TestNewExplicitSlugConflictFails(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	if code := cli.Run(context.Background(), []string{"new", "--slug", "custom slug", "First"}, cfg); code != cli.ExitOK {
		t.Fatalf("first new exit code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"new", "--slug", "custom-slug", "Second"}, cfg); code == cli.ExitOK || !strings.Contains(stderr.String(), "already taken") {
		t.Fatalf("expected slug conflict, code=%d stderr=%s", code, stderr.String())
	}
}

func TestNewRepoFlagUsesProvidedRepositoryPath(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	st := store.New(t.TempDir())
	commit := strings.Repeat("a", 40)
	fg := &fakeGit{branches: map[string]bool{}, info: gitpkg.RepositoryInfo{Repository: model.Repository{RootAtCreation: "/repos/other", GitCommonDir: "/repos/other/.git", CreatedFromCommit: commit}, Commit: commit}}
	application := app.New(st, fg)
	bindTestCoordinator(t, application, st)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 58), nil }
	cwd := t.TempDir()
	code := cli.Run(context.Background(), []string{"new", "--repo", "../other", "Other Repo Item"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: cwd, Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d stderr=%s", code, stderr.String())
	}
	want := filepath.Clean(filepath.Join(cwd, "../other"))
	if fg.firstDetectedDir != want {
		t.Fatalf("detected dir = %q, want %q", fg.firstDetectedDir, want)
	}
}

func TestNewHomeCLIRecordsBorrowedCheckout(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	st := store.New(t.TempDir())
	root := t.TempDir()
	commit := strings.Repeat("a", 40)
	fg := &fakeGit{branches: map[string]bool{}, info: gitpkg.RepositoryInfo{Repository: model.Repository{RootAtCreation: root, GitCommonDir: filepath.Join(root, ".git"), CreatedFromCommit: commit}, Commit: commit}}
	application := app.New(st, fg)
	bindTestCoordinator(t, application, st)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 59), nil }
	code := cli.Run(context.Background(), []string{"--json", "new", "--home", "Mainline"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: root, Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("exit code=%d stderr=%s", code, stderr.String())
	}
	var got struct {
		Manifest *model.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Manifest == nil || got.Manifest.Checkout.Kind != model.WorkspaceKindRepositoryHome || got.Manifest.Checkout.Path == nil || *got.Manifest.Checkout.Path != root {
		t.Fatalf("result=%+v", got)
	}
}

func TestItemFlagWorksForItemPrimaryCommands(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Flag Target", Description: "Hydrated show description", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	code := cli.Run(context.Background(), []string{"--json", "show", "--item", newRes.Manifest.ID}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("show --item exit code = %d stderr=%s", code, stderr.String())
	}
	var shown app.ShowResult
	if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil {
		t.Fatalf("invalid show json: %v\n%s", err, stdout.String())
	}
	if shown.Manifest.ID != newRes.Manifest.ID || shown.Description != "Hydrated show description" {
		t.Fatalf("shown = %+v", shown)
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"--json", "start", newRes.Manifest.Slug}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("start slug exit code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"--json", "workspace", "status", "--item", newRes.Manifest.ID}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("checkout status --item exit code = %d stderr=%s", code, stderr.String())
	}
	var checkout app.WorkspaceStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &checkout); err != nil {
		t.Fatalf("invalid checkout json: %v\n%s", err, stdout.String())
	}
	if checkout.WorkItemID != newRes.Manifest.ID || !checkout.Checkout.Present() {
		t.Fatalf("checkout = %+v", checkout)
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"--json", "switch", "--item", newRes.Manifest.ID}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("switch --item exit code = %d stderr=%s", code, stderr.String())
	}
	var switched app.CompositionResult
	if err := json.Unmarshal(stdout.Bytes(), &switched); err != nil || switched.WorkItemID != newRes.Manifest.ID || switched.Manifest.State != model.StateWorking {
		t.Fatalf("switch = %+v err=%v", switched, err)
	}
}

func TestPickerResumesSelectedWaitingItem(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Waiting Picker Target", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, model.StateWaiting, false); err != nil {
		t.Fatal(err)
	}
	fzf := filepath.Join(t.TempDir(), "fake-fzf")
	if err := os.WriteFile(fzf, []byte("#!/bin/sh\nhead -n 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	code := cli.Run(context.Background(), []string{"switch", "--no-agent", "--no-preview"}, cli.Config{
		Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_FZF": fzf}, App: application,
	})
	if code != cli.ExitOK {
		t.Fatalf("switch picker exit code=%d stderr=%s", code, stderr.String())
	}
	manifest, err := application.Store.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.State != model.StateWorking {
		t.Fatalf("selected waiting item state = %s", manifest.State)
	}
}

func TestWorkspaceStatusTextExplainsBranchMismatch(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Workspace Diagnostics", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	path := *started.Workspace.Manifest.Checkout.Path
	git := application.Git.(*fakeGit)
	git.worktrees[path] = "temporary-fix"
	code := cli.Run(context.Background(), []string{"workspace", "status", "--item", created.Manifest.ID}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("workspace status code=%d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{"worktree: problem", "reason: checkout branch temporary-fix differs from expected " + started.Workspace.Manifest.Checkout.Branch, "expected branch: " + started.Workspace.Manifest.Checkout.Branch, "current branch: temporary-fix", "created from commit:", "current HEAD:", "dirty: false"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("workspace status missing %q:\n%s", expected, output)
		}
	}
}

func TestItemFlagAndPositionalSelectorConflict(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	code := cli.Run(context.Background(), []string{"show", "--item", "one", "two"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitUsage {
		t.Fatalf("exit code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "either with --item or as a positional") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestNewInheritsUserAndProjectDefaultLabels(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	repo := application.Git.(*fakeGit).info.Repository.RootAtCreation
	if err := os.MkdirAll(filepath.Join(repo, ".config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".config", "wi.toml"), []byte("[item.defaults]\nlabels = [\"project\", \"shared\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	application.ItemConfig.Defaults.Labels = []string{"user", "shared"}
	code := cli.Run(context.Background(), []string{"--json", "new", "--label", "explicit", "Defaults"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: repo, Env: map[string]string{"WI_ITEM_DEFAULT_LABELS": "environment,shared"}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var got struct {
		Manifest model.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if labels := strings.Join(got.Manifest.Labels, ","); labels != "user,shared,project,environment,explicit" {
		t.Fatalf("labels = %q", labels)
	}
}

func TestNewPromptValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "empty", args: []string{"new", "--prompt", "", "Prompted"}, want: "--prompt requires a non-empty description"},
		{name: "conflict", args: []string{"new", "--prompt", "work now", "--desc-file", "handoff.md", "Prompted"}, want: "pass either --prompt or --desc-file, not both"},
		{name: "mode without prompt", args: []string{"new", "--agent-mode", "rpc", "Prompted"}, want: "--agent-mode requires --prompt"},
		{name: "invalid mode", args: []string{"new", "--agent-mode", "batch", "--prompt", "work now", "Prompted"}, want: "expected tui or rpc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, application := configuredApp(t)
			code := cli.Run(context.Background(), tc.args, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
			if code != cli.ExitUsage || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestNewNoDefaultLabels(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	application.ItemConfig.Defaults.Labels = []string{"user"}
	code := cli.Run(context.Background(), []string{"--json", "new", "--no-default-labels", "--label", "explicit", "No defaults"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ITEM_DEFAULT_LABELS": "environment"}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var got struct {
		Manifest model.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if labels := strings.Join(got.Manifest.Labels, ","); labels != "explicit" {
		t.Fatalf("labels = %q", labels)
	}
}

func TestStartNewCreatesAndStartsMinimalItem(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	application.ItemConfig.Defaults.Labels = []string{"default"}
	code := cli.Run(context.Background(), []string{"--json", "start", "--new", "--no-default-labels", "Add API retries"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result app.StartNewWorkItemResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.New.Manifest.Title != "Add API retries" || result.New.Manifest.Slug != "add-api-retries" || len(result.New.Manifest.Labels) != 0 {
		t.Fatalf("created manifest = %+v", result.New.Manifest)
	}
	if result.Start.Transition.State != model.StateWorking || result.Start.Workspace.AgentRuntime == nil {
		t.Fatalf("start result = %+v", result.Start)
	}
}

func TestStartNewSupportsRepositoryHome(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	code := cli.Run(context.Background(), []string{"--json", "start", "--new", "--home", "Mainline maintenance"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result app.StartNewWorkItemResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.New.Manifest.Checkout.Kind != model.WorkspaceKindRepositoryHome || result.Start.Transition.State != model.StateWorking {
		t.Fatalf("result = %+v", result)
	}
}

func TestStartCreationFlagsRequireNew(t *testing.T) {
	for _, flag := range []string{"--home", "--no-default-labels"} {
		stdout, stderr, application := configuredApp(t)
		code := cli.Run(context.Background(), []string{"start", flag, "existing"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
		if code != cli.ExitUsage || !strings.Contains(stderr.String(), "requires --new") {
			t.Fatalf("flag=%s code=%d stderr=%s", flag, code, stderr.String())
		}
	}
}

func TestStartAcceptsUniqueSlugSubstring(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Exact Existing Slug", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	for _, args := range [][]string{
		{"start", created.Manifest.ID},
		{"start", "Exact Existing Slug"},
		{"start", "--item", created.Manifest.Slug},
		{"start", "missing-slug"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := cli.Run(context.Background(), args, cfg); code != cli.ExitUsage && code != cli.ExitError {
			t.Fatalf("%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "start", "existing"}, cfg); code != cli.ExitOK {
		t.Fatalf("unique substring code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "start", created.Manifest.Slug}, cfg); code != cli.ExitOK {
		t.Fatalf("exact slug code=%d stderr=%s", code, stderr.String())
	}
}

func TestNewRejectsRemovedStartCompositionFlags(t *testing.T) {
	for _, flag := range []string{"--start", "--agent-mode", "--force"} {
		stdout, stderr, application := configuredApp(t)
		args := []string{"new", flag}
		if flag == "--agent-mode" {
			args = append(args, "rpc")
		}
		args = append(args, "item")
		if code := cli.Run(context.Background(), args, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}); code != cli.ExitUsage {
			t.Fatalf("new %s code=%d stdout=%s stderr=%s", flag, code, stdout.String(), stderr.String())
		}
	}
}

func TestNewDeepFlagSetsBuiltInDeepWork(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	code := cli.Run(context.Background(), []string{"--json", "new", "--deep", "Deep work item"}, cli.Config{
		Stdout: stdout,
		Stderr: stderr,
		CWD:    t.TempDir(),
		Env:    map[string]string{},
		App:    application,
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d stderr=%s", code, stderr.String())
	}
	var got struct {
		Manifest model.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, stdout.String())
	}
	if !got.Manifest.DeepWork || len(got.Manifest.Labels) != 0 {
		t.Fatalf("manifest deep_work=%v labels=%+v", got.Manifest.DeepWork, got.Manifest.Labels)
	}
}

func TestDeepCommandSetsAndClearsBuiltInDeepWork(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Deep Command", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ID": newRes.Manifest.ID}, App: application}
	code := cli.Run(context.Background(), []string{"--json", "deep"}, cfg)
	if code != cli.ExitOK {
		t.Fatalf("deep exit code = %d stderr=%s", code, stderr.String())
	}
	var set app.DeepWorkResult
	if err := json.Unmarshal(stdout.Bytes(), &set); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, stdout.String())
	}
	if !set.DeepWork || !set.Manifest.DeepWork || len(set.Manifest.Labels) != 0 {
		t.Fatalf("set = %+v", set)
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"list", "--no-agent"}, cfg)
	if code != cli.ExitOK {
		t.Fatalf("list exit code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "DEEP") || !strings.Contains(stdout.String(), "●") {
		t.Fatalf("list should show dedicated deep-work column and marker:\n%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"--json", "deep", "--clear"}, cfg)
	if code != cli.ExitOK {
		t.Fatalf("deep --clear exit code = %d stderr=%s", code, stderr.String())
	}
	var cleared app.DeepWorkResult
	if err := json.Unmarshal(stdout.Bytes(), &cleared); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, stdout.String())
	}
	if cleared.DeepWork || cleared.Manifest.DeepWork {
		t.Fatalf("cleared = %+v", cleared)
	}
}

func TestListOmitsStateAndMarksRepositoryHome(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	st := store.New(t.TempDir())
	root := t.TempDir()
	commit := strings.Repeat("a", 40)
	fg := &fakeGit{branches: map[string]bool{}, info: gitpkg.RepositoryInfo{Repository: model.Repository{RootAtCreation: root, GitCommonDir: filepath.Join(root, ".git"), CreatedFromCommit: commit}, Commit: commit}}
	application := app.New(st, fg)
	bindTestCoordinator(t, application, st)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 57), nil }
	if _, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Home row", Home: true, CWD: root}); err != nil {
		t.Fatal(err)
	}
	code := cli.Run(context.Background(), []string{"list", "--no-agent"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: root, Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "STATE") || !strings.Contains(out, "(home)") {
		t.Fatalf("list output:\n%s", out)
	}
}

func TestListShowsRepositoryLastColumn(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	st := store.New(t.TempDir())
	commit := strings.Repeat("a", 40)
	fg := &fakeGit{branches: map[string]bool{}, info: gitpkg.RepositoryInfo{
		Repository: model.Repository{RootAtCreation: t.TempDir(), GitCommonDir: t.TempDir() + "/.git", RemoteURL: "git@example.com:acme/repo.git", CreatedFromCommit: commit},
		Commit:     commit,
	}}
	application := app.New(st, fg)
	bindTestCoordinator(t, application, st)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 57), nil }
	code := cli.Run(context.Background(), []string{"new", "--label", "networking", "Repo Column"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("new exit code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"list"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("list exit code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "REPOSITORY") || !strings.Contains(out, "repo-column") || !strings.Contains(out, "networking") {
		t.Fatalf("list output missing expected columns:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "repo-column") && !strings.HasSuffix(line, "repo") {
			t.Fatalf("repository should be last column, line=%q", line)
		}
	}
}

func TestListHidesActiveIDsAndMarksCurrentItem(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Current Item", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ID": newRes.Manifest.ID}, App: application}
	if code := cli.Run(context.Background(), []string{"list", "--no-agent"}, cfg); code != cli.ExitOK {
		t.Fatalf("list exit code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "› current-item") {
		t.Fatalf("list should mark current item:\n%s", out)
	}
	if strings.Contains(out, "  ID") || strings.Contains(out, model.ShortID(newRes.Manifest.ID)) {
		t.Fatalf("active list should hide ID column by default:\n%s", out)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"list", "--no-agent", "--ids"}, cfg); code != cli.ExitOK {
		t.Fatalf("list --ids exit code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "  ID") || !strings.Contains(stdout.String(), model.ShortID(newRes.Manifest.ID)) {
		t.Fatalf("list --ids should show ID column:\n%s", stdout.String())
	}
}

func TestListShowsUniqueIDPrefixes(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	st := store.New(t.TempDir())
	commit := strings.Repeat("a", 40)
	fg := &fakeGit{branches: map[string]bool{}, info: gitpkg.RepositoryInfo{
		Repository: model.Repository{RootAtCreation: t.TempDir(), GitCommonDir: t.TempDir() + "/.git", CreatedFromCommit: commit},
		Commit:     commit,
	}}
	application := app.New(st, fg)
	bindTestCoordinator(t, application, st)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{
		"01KYTAXAJ97CYKWY9XF9Y2SCJ0",
		"01KYTAM0AZBTR8NRVVTTZJQJRQ",
	}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	if code := cli.Run(context.Background(), []string{"--json", "new", "new test"}, cfg); code != cli.ExitOK {
		t.Fatalf("first new exit code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "new", "new test"}, cfg); code != cli.ExitOK {
		t.Fatalf("second new exit code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"list", "--ids"}, cfg); code != cli.ExitOK {
		t.Fatalf("list exit code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "01KYTAX") || !strings.Contains(out, "01KYTAM") {
		t.Fatalf("list did not show unique prefixes:\n%s", out)
	}
}

func TestListLabelRulesMergeConfigEnvironmentAndCLI(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	for _, item := range []app.NewWorkItemOptions{
		{Title: "Team Jira", CWD: t.TempDir(), Labels: []string{"team", "jira"}},
		{Title: "Team Personal", CWD: t.TempDir(), Labels: []string{"team", "personal"}},
		{Title: "Jira Only", CWD: t.TempDir(), Labels: []string{"jira"}},
	} {
		if _, err := application.NewWorkItem(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	application.ListConfig.Labels = []string{"+team", "-personal"}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_LIST_LABELS": "+personal,-jira"}, App: application}
	if code := cli.Run(context.Background(), []string{"list", "--no-agent"}, cfg); code != cli.ExitOK {
		t.Fatalf("list exit code = %d stderr=%s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "team-personal") || strings.Contains(out, "team-jira") || strings.Contains(out, "jira-only") {
		t.Fatalf("environment did not override same-label config rules:\n%s", out)
	}

	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"list", "--no-agent", "--label", "+jira", "--label", "-personal"}, cfg); code != cli.ExitOK {
		t.Fatalf("list override exit code = %d stderr=%s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "team-jira") || strings.Contains(out, "team-personal") || strings.Contains(out, "jira-only") {
		t.Fatalf("CLI did not override environment rules:\n%s", out)
	}

	stdout.Reset()
	stderr.Reset()
	cfg.Env["WI_LIST_LABELS"] = "!,+jira"
	if code := cli.Run(context.Background(), []string{"list", "--no-agent"}, cfg); code != cli.ExitOK {
		t.Fatalf("list reset exit code = %d stderr=%s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "team-jira") || !strings.Contains(out, "jira-only") || strings.Contains(out, "team-personal") {
		t.Fatalf("environment reset did not clear config rules:\n%s", out)
	}
}

func TestAgentStatusWithoutCurrentItemShowsOverview(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Status Overview", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false); err != nil {
		t.Fatal(err)
	}
	code := cli.Run(context.Background(), []string{"agent", "status", "--all"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("agent status exit code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "AGENT STATUS") || !strings.Contains(stdout.String(), model.ShortID(newRes.Manifest.ID)) {
		t.Fatalf("overview output = %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"agent", "status"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
	if code == cli.ExitOK || strings.Contains(stdout.String(), "AGENT STATUS") {
		t.Fatalf("agent status without selection should fail explicitly: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestLabelShorthandWithItemFlagAddsLabels(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	first, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Current Item", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Target Item", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	code := cli.Run(context.Background(), []string{"label", "--item", "target", "networking"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ID": first.Manifest.ID}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("label exit code = %d stderr=%s", code, stderr.String())
	}
	firstLabels, err := application.ListLabels(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	secondLabels, err := application.ListLabels(context.Background(), app.ResolveOptions{Selector: second.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstLabels.Labels) != 0 {
		t.Fatalf("first labels = %+v", firstLabels.Labels)
	}
	if len(secondLabels.Labels) != 1 || secondLabels.Labels[0] != "networking" {
		t.Fatalf("second labels = %+v", secondLabels.Labels)
	}
}

func TestLabelRemoveFlagAndListDefault(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	res, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Current Item", CWD: t.TempDir(), Labels: []string{"networking", "backend"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{"WI_ID": res.Manifest.ID}, App: application}
	code := cli.Run(context.Background(), []string{"label", "--remove", "backend"}, cfg)
	if code != cli.ExitOK {
		t.Fatalf("label remove exit code = %d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"label"}, cfg)
	if code != cli.ExitOK {
		t.Fatalf("label list exit code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "networking") || strings.Contains(out, "backend") {
		t.Fatalf("labels output = %q", out)
	}
}

func TestMergeJSON(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	runCLIGit(t, repo, "init", "-b", "main")
	runCLIGit(t, repo, "config", "user.email", "test@example.com")
	runCLIGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, repo, "add", "README.md")
	runCLIGit(t, repo, "commit", "-m", "initial")
	st := store.New(t.TempDir())
	application := app.New(st, gitpkg.New("git"))
	bindTestCoordinator(t, application, st)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 93), testutil.ID(t, 94)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "CLI Merge", CWD: repo})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID, CWD: repo}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	source := *started.Workspace.Checkout.Path
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, source, "add", "feature.txt")
	runCLIGit(t, source, "commit", "-m", "feature")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := cli.Run(context.Background(), []string{"--json", "merge", "--item", created.Manifest.ID, "main"}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: source, Env: map[string]string{}, App: application})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d stderr=%s", code, stderr.String())
	}
	var got app.MergeResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if got.WorkItemID != created.Manifest.ID || got.TargetBranch != "main" || !got.TargetAdvanced || !got.TargetSynced || got.SourceNewSHA == "" {
		t.Fatalf("merge result = %+v", got)
	}
}

func runCLIGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestNextRejectsMultipleCurrentItemMutations(t *testing.T) {
	for _, args := range [][]string{{"next", "--wait", "--defer"}, {"next", "--archive", "--wait"}, {"next", "--archive", "--defer"}} {
		stdout, stderr, application := configuredApp(t)
		code := cli.Run(context.Background(), args, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application})
		if code != cli.ExitUsage || !strings.Contains(stderr.String(), "only one of --defer, --wait, or --archive") {
			t.Fatalf("args=%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestNewDependentFlagsAndLiteralDelimiter(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	for _, args := range [][]string{{"new", "--no-attach", "invalid"}, {"new", "--force", "invalid"}} {
		stdout.Reset()
		stderr.Reset()
		if code := cli.Run(context.Background(), args, cfg); code != cli.ExitUsage {
			t.Fatalf("%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"new", "--", "--json"}, cfg); code != cli.ExitOK {
		t.Fatalf("literal --json code=%d stderr=%s", code, stderr.String())
	}
	if strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") {
		t.Fatalf("literal --json was consumed as a global flag: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"new", "--", "--help"}, cfg); code != cli.ExitOK {
		t.Fatalf("literal --help code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("literal --help triggered help: %s", stdout.String())
	}
}

func TestListArchivedStateAndConflicts(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Archived Filter", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Archive(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false); err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	if code := cli.Run(context.Background(), []string{"--json", "list", "--state", "archived"}, cfg); code != cli.ExitOK {
		t.Fatalf("archived state code=%d stderr=%s", code, stderr.String())
	}
	var list app.WorkListResult
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sections.Archived) != 1 || list.Sections.Archived[0].ID != created.Manifest.ID {
		t.Fatalf("archived list = %+v", list.Sections.Archived)
	}
	for _, args := range [][]string{{"list", "--all", "--archived"}, {"list", "--archived", "--state", "working"}, {"list", "--state", "invalid"}} {
		stdout.Reset()
		stderr.Reset()
		if code := cli.Run(context.Background(), args, cfg); code != cli.ExitUsage {
			t.Fatalf("%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestDeleteArchivedRequiresConfirmationAndSupportsBulk(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	for _, title := range []string{"Archived One", "Archived Two"} {
		created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: title, CWD: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := application.Archive(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false); err != nil {
			t.Fatal(err)
		}
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	if code := cli.Run(context.Background(), []string{"delete", "--archived"}, cfg); code != cli.ExitUsage {
		t.Fatalf("delete without confirmation code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "delete", "--archived", "--yes"}, cfg); code != cli.ExitOK {
		t.Fatalf("bulk delete code=%d stderr=%s", code, stderr.String())
	}
	var result app.DeleteWorkItemsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 {
		t.Fatalf("result=%+v", result)
	}
	items, errs := application.Store.ListManifests()
	if len(errs) != 0 || len(items) != 0 {
		t.Fatalf("remaining=%+v errors=%+v", items, errs)
	}
}

func TestDeepAcceptsPositionalItem(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Positional Deep", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	if code := cli.Run(context.Background(), []string{"--json", "deep", created.Manifest.ID}, cfg); code != cli.ExitOK {
		t.Fatalf("deep code=%d stderr=%s", code, stderr.String())
	}
	var got app.DeepWorkResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.DeepWork || got.WorkItemID != created.Manifest.ID {
		t.Fatalf("deep result = %+v", got)
	}
}

func TestCoreStatePrimitiveLeavesWorkspaceUntouched(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Core State Primitive", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	for _, target := range []string{model.StateWorking, model.StateWaiting, model.StateWorking, model.StateArchived, model.StateBacklog} {
		stdout.Reset()
		stderr.Reset()
		if code := cli.Run(context.Background(), []string{"--json", "state", "set", target, "--item", created.Manifest.ID}, cfg); code != cli.ExitOK {
			t.Fatalf("state set %s code=%d stderr=%s", target, code, stderr.String())
		}
		var got app.StateTransitionResult
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.State != target {
			t.Fatalf("state set %s = %+v", target, got)
		}
	}
	manifest, err := application.Store.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.State != model.StateBacklog || manifest.Slug == "" || manifest.Checkout.Present() || manifest.TerminalSessionName() != created.Manifest.TerminalSessionName() {
		t.Fatalf("manifest after core state mutation = %+v", manifest)
	}
	mux := application.Tmux.(*fakeTmux)
	if len(mux.ensured) != 0 || len(mux.killed) != 0 {
		t.Fatalf("core state mutation touched tmux: ensured=%v killed=%v", mux.ensured, mux.killed)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "state", "show", created.Manifest.ID}, cfg); code != cli.ExitOK {
		t.Fatalf("state show code=%d stderr=%s", code, stderr.String())
	}
	var shown app.WorkItemStateResult
	if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil || shown.State != model.StateBacklog {
		t.Fatalf("state show = %+v err=%v", shown, err)
	}
}

func TestCoreWorkspacePrimitiveLeavesLifecycleStateUntouched(t *testing.T) {
	stdout, stderr, application := configuredApp(t)
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Core Workspace Primitive", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}
	if code := cli.Run(context.Background(), []string{"--json", "workspace", "ensure", "--item", created.Manifest.ID}, cfg); code != cli.ExitOK {
		t.Fatalf("workspace ensure code=%d stderr=%s", code, stderr.String())
	}
	manifest, err := application.Store.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.State != model.StateBacklog || !manifest.Checkout.Present() || manifest.RootPiSession != nil {
		t.Fatalf("workspace ensure created non-worktree resources: %+v", manifest)
	}
	if len(application.Tmux.(*fakeTmux).ensured) != 0 {
		t.Fatal("workspace ensure should not create terminal access")
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "terminal", "ensure", "--item", created.Manifest.ID}, cfg); code != cli.ExitOK {
		t.Fatalf("terminal ensure code=%d stderr=%s", code, stderr.String())
	}
	if manifest, err = application.Store.LoadManifest(created.Manifest.ID); err != nil || manifest.RootPiSession != nil {
		t.Fatalf("terminal ensure created an agent conversation: manifest=%+v err=%v", manifest, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "state", "set", model.StateArchived, "--item", created.Manifest.ID}, cfg); code != cli.ExitOK {
		t.Fatalf("state archive code=%d stderr=%s", code, stderr.String())
	}
	manifest, err = application.Store.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.State != model.StateArchived || !manifest.Checkout.Present() || !application.Tmux.(*fakeTmux).sessions[manifest.TerminalSessionName()] {
		t.Fatalf("state-only archive changed workspace = %+v", manifest)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "terminal", "close", "--item", created.Manifest.ID}, cfg); code != cli.ExitOK {
		t.Fatalf("terminal close code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"--json", "workspace", "release", "--item", created.Manifest.ID}, cfg); code != cli.ExitOK {
		t.Fatalf("workspace release code=%d stderr=%s", code, stderr.String())
	}
	manifest, err = application.Store.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.State != model.StateArchived || manifest.Checkout.Present() {
		t.Fatalf("released manifest = %+v; release result=%s", manifest, stdout.String())
	}
}

func TestRemovedTopLevelPlumbingAliasesAreUnknown(t *testing.T) {
	for _, command := range []string{"activity", "defer", "wait", "unarchive", "checkout"} {
		t.Run(command, func(t *testing.T) {
			stdout, stderr, application := configuredApp(t)
			if code := cli.Run(context.Background(), []string{command}, cli.Config{Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}, App: application}); code != cli.ExitUsage || !strings.Contains(stderr.String(), "unknown command") {
				t.Fatalf("%s code/output = %d stdout=%s stderr=%s", command, code, stdout.String(), stderr.String())
			}
		})
	}
}

func bindTestCoordinator(t *testing.T, application *app.App, st *store.Store) {
	t.Helper()
	socket := filepath.Join(testutil.ShortTempDir(t), "daemon.sock")
	server, err := coordinator.NewServer(st.Root, socket)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	client := &coordinator.Client{SocketPath: socket}
	pingCtx, pingCancel := context.WithTimeout(context.Background(), time.Second)
	if err := client.Ping(pingCtx); err != nil {
		pingCancel()
		server.Close()
		<-serveDone
		t.Fatal(err)
	}
	pingCancel()
	t.Cleanup(func() {
		server.Close()
		<-serveDone
	})
	cli.BindCoordinatorApp(application, st, client)
}

func configuredApp(t *testing.T) (*bytes.Buffer, *bytes.Buffer, *app.App) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := t.TempDir()
	st := store.New(root)
	commit := strings.Repeat("a", 40)
	fg := &fakeGit{branches: map[string]bool{}, info: gitpkg.RepositoryInfo{
		Repository: model.Repository{
			RootAtCreation:    t.TempDir(),
			GitCommonDir:      t.TempDir() + "/.git",
			RemoteURL:         "",
			CreatedFromCommit: commit,
		},
		Commit: commit,
	}}
	application := app.New(st, fg)
	socket := filepath.Join(testutil.ShortTempDir(t), "daemon.sock")
	server, err := coordinator.NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	client := &coordinator.Client{SocketPath: socket}
	pingCtx, pingCancel := context.WithTimeout(context.Background(), time.Second)
	if err := client.Ping(pingCtx); err != nil {
		pingCancel()
		server.Close()
		<-serveDone
		t.Fatal(err)
	}
	pingCancel()
	t.Cleanup(func() {
		server.Close()
		<-serveDone
	})
	cli.BindCoordinatorApp(application, st, client)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 13), testutil.ID(t, 14), testutil.ID(t, 15), testutil.ID(t, 16)}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	return stdout, stderr, application
}
