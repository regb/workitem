package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestAgentProjectTrustRequiresVerifiedRegisteredManagedWorktree(t *testing.T) {
	for _, mode := range []string{"verified", "archived repository", "unregistered", "legacy", "unmaterialized", "outside managed root", "wrong directory", "wrong Git store", "symlink escape"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			native := store.New(root)
			if err := native.Ensure(); err != nil {
				t.Fatal(err)
			}
			source := t.TempDir()
			runGit := func(dir string, args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, output)
				}
			}
			runGit(source, "init", "-b", "main")
			runGit(source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
			repoID, _ := model.NewID()
			itemID, _ := model.NewID()
			slot := "slot-0001"
			bare, _ := model.OwnedRepositoryGitDir(root, repoID)
			git := gitpkg.New("git")
			if err := git.ImportBare(ctx, model.BareImportOptions{Source: source, Destination: bare, IntegrationBranch: "main"}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(native.WorktreesDir(), repoID, slot)
			runGit(bare, "worktree", "add", "-b", "task", path, "main")
			socket := filepath.Join(testutil.ShortTempDir(t), "daemon.sock")
			server, err := coordinator.NewServer(root, socket)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx) }()
			client := &coordinator.Client{SocketPath: socket}
			t.Cleanup(func() { client.Shutdown(context.Background()); <-done })
			if mode != "unregistered" {
				now := time.Now().UTC()
				if _, err := client.RegisterRepository(ctx, coordinator.RegisterRepositoryCommand{ID: "register", ProtocolVersion: coordinator.ProtocolVersion, Repository: model.OwnedRepository{ID: repoID, Name: "project", IntegrationBranch: "main", CreatedAt: now}, CreatedAt: now}); err != nil {
					t.Fatal(err)
				}
				if mode == "archived repository" {
					if _, err := client.SetRepositoryArchived(ctx, coordinator.SetRepositoryArchivedCommand{ID: "archive", ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: repoID, Archived: true, CreatedAt: now}); err != nil {
						t.Fatal(err)
					}
				}
			}
			head, err := git.Head(ctx, bare)
			if err != nil {
				t.Fatal(err)
			}
			m := model.NewManifest(itemID, "task", "Task", nil, false, time.Now(), model.Repository{ID: repoID, CreatedFromCommit: head}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "task"))
			if _, err := m.Checkout.AssignNewClaim(model.ExecutorLocal, repoID, slot, path); err != nil {
				t.Fatal(err)
			}
			cwd := path
			switch mode {
			case "legacy":
				m.Repository.ID = ""
			case "unmaterialized":
				m.Checkout.Claim = nil
			case "outside managed root":
				m.Checkout.Claim.Path = source
				cwd = source
			case "wrong directory":
				cwd = source
			case "wrong Git store":
				runGit(bare, "worktree", "remove", "--force", path)
				runGit(source, "worktree", "add", "-b", "different-store", path, "main")
			case "symlink escape":
				outside := filepath.Join(t.TempDir(), "escaped")
				runGit(bare, "worktree", "move", path, outside)
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := native.CreateItem(ctx, m); err != nil {
				t.Fatal(err)
			}
			application := &App{Store: native, Git: git, DaemonSocketPath: socket}
			approved, err := application.agentProjectTrusted(ctx, itemID, cwd)
			want := mode == "verified" || mode == "archived repository"
			if approved != want {
				t.Fatalf("approved=%v want=%v err=%v", approved, want, err)
			}
			if want && err != nil {
				t.Fatal(err)
			}
			if mode == "verified" {
				m.State = model.StateWorking
				sessionID, _ := model.NewID()
				m.PrimaryConversation = &model.Conversation{Driver: "pi", ID: sessionID, Locator: "sessions/pi/" + sessionID + ".jsonl"}
				if err := native.SaveManifest(ctx, m); err != nil {
					t.Fatal(err)
				}
				application = New(native, git)
				application.DefaultAgent = "pi"
				application.AgentsConfig["pi"] = config.AgentDefinitionConfig{Driver: "pi", Config: map[string]any{}}
				application.DaemonSocketPath = socket
				application.AgentRuntimeSocketRoot = testutil.ShortTempDir(t)
				application.BundledDriverCommands["pi"] = BundledDriverCommand{Path: "test-driver"}
				application.AgentConfig.AppendSystemPrompt = "Shared custom system instructions\nfor either mode."
				for _, runtimeMode := range []agent.Mode{agent.ModeTUI, agent.ModeHeadless} {
					runtime := model.AgentRuntime{ID: string(runtimeMode), WorkItemID: itemID, ConversationID: sessionID, Mode: string(runtimeMode), State: model.AgentRuntimeStarting, WorkspaceClaimID: m.Checkout.Claim.ID, WorkspaceClaimGeneration: m.Checkout.Claim.Generation}
					if err := native.SaveAgentRuntime(ctx, itemID, runtime); err != nil {
						t.Fatal(err)
					}
					called := false
					application.RunAgentDriver = func(_ context.Context, _ string, _ []string, request driverprotocol.LaunchRequest, _ []string, onReady func(driverprotocol.Ready) error) error {
						called = true
						if request.AppendSystemPrompt != application.AgentConfig.AppendSystemPrompt {
							t.Fatal("launch lost system prompt")
						}
						if !request.ProjectTrusted || request.Runtime.Mode != driverprotocol.Mode(runtimeMode) {
							t.Fatalf("unapproved launch=%+v", request)
						}
						return onReady(driverprotocol.Ready{ProtocolVersion: driverprotocol.Version})
					}
					if err := application.AgentExec(ctx, itemID, sessionID, runtime.ID, runtimeMode); err != nil {
						t.Fatal(err)
					}
					if !called {
						t.Fatal("driver was not launched")
					}
				}
			}
		})
	}
}
