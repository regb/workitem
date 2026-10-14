package cli

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
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func remoteTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func remoteTestCommit(t *testing.T, dir, file, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	remoteTestGit(t, dir, "add", file)
	remoteTestGit(t, dir, "commit", "-m", message)
	return remoteTestGit(t, dir, "rev-parse", "HEAD")
}
func remoteCLI(t *testing.T) (Config, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	origin := t.TempDir()
	remoteTestGit(t, origin, "init", "-b", "main")
	remoteTestGit(t, origin, "config", "user.name", "Test")
	remoteTestGit(t, origin, "config", "user.email", "test@example.com")
	remoteTestCommit(t, origin, "document", "base\n", "base")
	remoteTestGit(t, origin, "remote", "add", "origin", origin)
	socket, err := coordinator.SocketPath(testutil.ShortTempDir(t), root)
	if err != nil {
		t.Fatal(err)
	}
	agentSocket, err := coordinator.AgentSocketPath(testutil.ShortTempDir(t), root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServerWithAgentSocket(root, socket, agentSocket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	t.Cleanup(func() { server.Close(); <-done })
	native := store.New(root)
	application := app.New(native, gitpkg.New("git"))
	application.DefaultAgent = "pi"
	application.AgentsConfig["pi"] = config.AgentDefinitionConfig{Driver: "pi", Config: map[string]any{}}
	client := &coordinator.Client{SocketPath: socket}
	BindCoordinatorApp(application, native, client)
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cfg := Config{RequireOwnedRepository: true, App: application, Coordinator: client, CWD: origin, Env: map[string]string{}, Stdout: out, Stderr: stderr}
	if err := runRepository(context.Background(), []string{"add", "project", "--from", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{coordinator.MethodSetRepositoryUpstream, coordinator.MethodRepositoryStatus, coordinator.MethodRepositorySync} {
		agent := &coordinator.Client{SocketPath: agentSocket}
		var result any
		if err := agent.Call(context.Background(), method, map[string]string{"selector": "project"}, &result); err == nil {
			t.Fatalf("agent endpoint accepted %s", method)
		}
	}
	out.Reset()
	stderr.Reset()
	return cfg, origin, out, stderr
}

func TestRepositoryRemoteCLIConfigurationStatusAndSync(t *testing.T) {
	cfg, origin, out, stderr := remoteCLI(t)
	ctx := context.Background()
	source, err := cfg.Coordinator.RepositoryViewSource(ctx, "project", "")
	if err != nil || source.Repository.Upstream == nil || source.Repository.Upstream.URL != origin || source.Repository.Upstream.Branch != "main" {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "tracks "+origin+" branch main") {
		t.Fatal(out.String())
	}
	out.Reset()
	remote := remoteTestCommit(t, origin, "remote", "remote\n", "remote")
	if err := runRepository(ctx, []string{"status", "project", "--fetch"}, cfg, true); err != nil {
		t.Fatal(err)
	}
	var status coordinator.RepositoryStatusResult
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status.State != "behind" {
		t.Fatalf("status=%+v err=%v out=%s", status, err, out.String())
	}
	out.Reset()
	if err := runRepository(ctx, []string{"sync", "project"}, cfg, true); err != nil {
		t.Fatal(err)
	}
	var result coordinator.RepositorySyncResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.LocalSHA != remote || !result.Changed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	out.Reset()
	for _, args := range [][]string{{"sync", "project", "--reset-to-upstream"}, {"sync", "project", "--merge", "--reset-to-upstream"}, {"sync", "project", "--yes"}, {"status", "project", "--merge"}, {"upstream", "project", "https://user:secret@example.com/project.git"}} {
		if err := runRepository(ctx, args, cfg, false); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	// A local-only commit demonstrates that reset confirms exact tips and keeps
	// a recovery ref without changing the source checkout or remote.
	path := filepath.Join(t.TempDir(), "local")
	remoteTestGit(t, source.GitDir, "config", "user.name", "Test")
	remoteTestGit(t, source.GitDir, "config", "user.email", "test@example.com")
	remoteTestGit(t, source.GitDir, "worktree", "add", "--detach", path, "main")
	local := remoteTestCommit(t, path, "local", "local\n", "local")
	remoteTestGit(t, source.GitDir, "update-ref", "refs/heads/main", local, remote)
	remoteTestGit(t, source.GitDir, "worktree", "remove", path)
	if err := runRepository(ctx, []string{"sync", "project", "--reset-to-upstream", "--yes"}, cfg, true); err != nil {
		t.Fatal(err)
	}
	result = coordinator.RepositorySyncResult{}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.LocalSHA != remote || result.RecoveryRef == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !strings.Contains(stderr.String(), local) || remoteTestGit(t, source.GitDir, "rev-parse", result.RecoveryRef) != local {
		t.Fatal("discarded commit not previewed and protected")
	}
	if err := runRepository(ctx, []string{"upstream", "project", "--clear"}, cfg, false); err != nil {
		t.Fatal(err)
	}
	source, err = cfg.Coordinator.RepositoryViewSource(ctx, "project", "")
	if err != nil || source.Repository.Upstream != nil {
		t.Fatal("upstream not cleared")
	}
}

func newRemoteMergeItem(t *testing.T, cfg Config) (model.Manifest, string) {
	t.Helper()
	var out bytes.Buffer
	cfg.Stdout = &out
	if code := Run(context.Background(), []string{"new", "--repo", "project", "--title", "Feature", "--json"}, cfg); code != ExitOK {
		t.Fatalf("new code=%d stderr=%v", code, cfg.Stderr)
	}
	var result struct {
		Manifest model.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run(context.Background(), []string{"workspace", "ensure", "--item", result.Manifest.ID}, cfg); code != ExitOK {
		t.Fatalf("workspace code=%d stderr=%v", code, cfg.Stderr)
	}
	out.Reset()
	if code := Run(context.Background(), []string{"state", "set", "working", "--item", result.Manifest.ID}, cfg); code != ExitOK {
		t.Fatalf("state code=%d stderr=%v", code, cfg.Stderr)
	}
	manifest, err := cfg.App.Store.LoadManifest(result.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := manifest.Checkout.Claim.Path
	remoteTestGit(t, path, "config", "user.name", "Test")
	remoteTestGit(t, path, "config", "user.email", "test@example.com")
	return manifest, path
}

func TestMergeSynchronizesUpstreamBeforeRebasingEquivalentItem(t *testing.T) {
	cfg, origin, _, _ := remoteCLI(t)
	ctx := context.Background()
	if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	manifest, path := newRemoteMergeItem(t, cfg)
	local := remoteTestCommit(t, path, "feature", "same feature\n", "local feature")
	remote := remoteTestCommit(t, origin, "feature", "same feature\n", "published feature")
	result, err := cfg.App.MergeWorkItem(ctx, app.MergeOptions{Selector: manifest.ID, CWD: path})
	if err != nil {
		t.Fatal(err)
	}
	if result.UpstreamSync == nil || !result.UpstreamSync.Changed || result.SourceNewSHA != remote || result.SourceNewSHA == local {
		t.Fatalf("result=%+v", result)
	}
	if remoteTestGit(t, path, "rev-parse", "main") != remote || remoteTestGit(t, origin, "rev-parse", "HEAD") != remote {
		t.Fatal("published commit IDs not preserved")
	}
}

func TestMergeLocalBypassesNetworkAndDefaultRefusesDivergence(t *testing.T) {
	for _, test := range []string{"unavailable", "diverged"} {
		t.Run(test, func(t *testing.T) {
			cfg, origin, _, _ := remoteCLI(t)
			ctx := context.Background()
			if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
				t.Fatal(err)
			}
			manifest, path := newRemoteMergeItem(t, cfg)
			local := remoteTestCommit(t, path, "feature", "feature\n", "feature")
			source, err := cfg.Coordinator.RepositoryViewSource(ctx, "project", "")
			if err != nil {
				t.Fatal(err)
			}
			old := source.Commit
			if test == "unavailable" {
				if err := runRepository(ctx, []string{"upstream", "project", filepath.Join(t.TempDir(), "missing")}, cfg, false); err != nil {
					t.Fatal(err)
				}
			} else {
				remoteTestGit(t, source.GitDir, "update-ref", "refs/heads/main", local, old)
				remoteTestCommit(t, origin, "remote", "remote\n", "remote")
			}
			before := remoteTestGit(t, source.GitDir, "rev-parse", "main")
			if _, err := cfg.App.MergeWorkItem(ctx, app.MergeOptions{Selector: manifest.ID, CWD: path}); err == nil {
				t.Fatal("unsafe default merge accepted")
			}
			if remoteTestGit(t, path, "rev-parse", "HEAD") != local || remoteTestGit(t, source.GitDir, "rev-parse", "main") != before {
				t.Fatal("failed preflight changed item or main")
			}
			result, err := cfg.App.MergeWorkItem(ctx, app.MergeOptions{Selector: manifest.ID, CWD: path, Local: true})
			if err != nil || result.UpstreamSync != nil {
				t.Fatalf("local result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRepositoryRemoteRejectsUpstreamChangeDuringConflict(t *testing.T) {
	cfg, origin, _, _ := remoteCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	manifest, path := newRemoteMergeItem(t, cfg)
	local := remoteTestCommit(t, path, "document", "local\n", "local")
	source, err := cfg.Coordinator.RepositoryViewSource(ctx, "project", "")
	if err != nil {
		t.Fatal(err)
	}
	remoteTestGit(t, source.GitDir, "update-ref", "refs/heads/main", local, source.Commit)
	remoteTestCommit(t, origin, "document", "remote\n", "remote")
	if err := runRepository(ctx, []string{"sync", "project", "--merge"}, cfg, false); err == nil {
		t.Fatal("expected conflict")
	}
	if err := runRepository(ctx, []string{"upstream", "project", "--clear"}, cfg, false); err == nil {
		t.Fatal("pending upstream cleared")
	}
	if _, err := cfg.App.MergeWorkItem(ctx, app.MergeOptions{Selector: manifest.ID, CWD: path, Local: true}); err == nil {
		t.Fatal("local merge bypassed pending reconciliation")
	}
	if err := runRepository(ctx, []string{"sync", "project", "--abort"}, cfg, false); err != nil {
		t.Fatal(err)
	}
}
