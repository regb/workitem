package coordinator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
)

func repositoryTestCommand(id, name string) RegisterRepositoryCommand {
	now := time.Now().UTC()
	return RegisterRepositoryCommand{ID: "register:" + id, ProtocolVersion: ProtocolVersion, Repository: model.OwnedRepository{ID: id, Name: name, IntegrationBranch: "main", CreatedAt: now}, CreatedAt: now}
}

func TestRepositoryRegistrationThroughOperatorEndpoint(t *testing.T) {
	root, runtime := t.TempDir(), t.TempDir()
	socket, err := SocketPath(runtime, root)
	if err != nil {
		t.Fatal(err)
	}
	agentSocket, err := AgentSocketPath(runtime, root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServerWithAgentSocket(root, socket, agentSocket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := &Client{SocketPath: socket}
	t.Cleanup(func() { _ = client.Shutdown(context.Background()); <-done })
	command := repositoryTestCommand(importTestID, "repo")
	if _, err := client.RegisterRepository(ctx, command); err == nil {
		t.Fatal("registered missing Git store")
	}
	source := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init", "-b", "main")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	path, err := model.OwnedRepositoryGitDir(root, importTestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitpkg.New("git").ImportBare(ctx, model.BareImportOptions{Source: source, Destination: path, IntegrationBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	result, err := client.RegisterRepository(ctx, command)
	if err != nil || result.Repository.ID != importTestID {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	repositories, err := client.ListRepositories(ctx)
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories=%+v error=%v", repositories, err)
	}
	rename := renameTestCommand("endpoint", "repo", "renamed")
	renamed, err := client.RenameRepository(ctx, rename)
	if err != nil || !renamed.Changed || renamed.Repository.ID != importTestID {
		t.Fatalf("rename=%+v error=%v", renamed, err)
	}
	sourceResult, err := client.RepositorySource(ctx, "renamed", "")
	if err != nil || sourceResult.GitDir != path || sourceResult.Repository.ID != importTestID {
		t.Fatalf("renamed source=%+v error=%v", sourceResult, err)
	}
	for _, base := range []string{"HEAD", "HEAD~1", "@", "@{-1}", "-", "main@{0}"} {
		if _, err := client.RepositorySource(ctx, "renamed", base); err == nil {
			t.Fatalf("resolved contextual base %q in bare store", base)
		}
	}
	integration, err := client.RepositorySource(ctx, "renamed", "^")
	if err != nil || integration.Commit != sourceResult.Commit {
		t.Fatalf("integration shorthand=%+v error=%v", integration, err)
	}
	if _, err := client.RepositorySource(ctx, "repo", ""); err == nil {
		t.Fatal("old name still resolves")
	}
	archive := archiveTestCommand("endpoint", false, true)
	archived, err := client.SetRepositoryArchived(ctx, archive)
	if err != nil || !archived.Repository.Archived {
		t.Fatalf("archive=%+v err=%v", archived, err)
	}
	archivedSource, err := client.RepositorySource(ctx, "renamed", "")
	if err != nil || !archivedSource.Repository.Archived || archivedSource.Commit != sourceResult.Commit {
		t.Fatalf("archived source=%+v err=%v", archivedSource, err)
	}
	agent := &Client{SocketPath: agentSocket}
	if _, err := agent.SetRepositoryArchived(ctx, archive); err == nil {
		t.Fatal("agent archived repository")
	}
	restore := archiveTestCommand("restore endpoint", true, false)
	if _, err := agent.SetRepositoryArchived(ctx, restore); err == nil {
		t.Fatal("agent restored repository")
	}
	if result, err := client.SetRepositoryArchived(ctx, restore); err != nil || result.Repository.Archived {
		t.Fatalf("restore=%+v err=%v", result, err)
	}
	if _, err := agent.RenameRepository(ctx, rename); err == nil {
		t.Fatal("agent renamed repository")
	}
	if _, err := agent.RegisterRepository(ctx, command); err == nil {
		t.Fatal("agent registered repository")
	}
	if _, err := agent.ListRepositories(ctx); err == nil {
		t.Fatal("agent read private registry")
	}
	if _, err := os.Stat(filepath.Join(path, "config")); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRegistrationIsAtomicAndIdempotent(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	command := repositoryTestCommand(importTestID, "fleet")
	first, err := db.RegisterRepository(command)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := db.RegisterRepository(command)
	if err != nil || !duplicate.Duplicate || duplicate.Sequence != first.Sequence {
		t.Fatalf("duplicate=%+v error=%v", duplicate, err)
	}
	changed := command
	changed.Repository.Name = "different-name"
	if _, err := db.RegisterRepository(changed); err == nil {
		t.Fatal("accepted changed command input")
	}
	repositories, err := db.ListRepositories()
	if err != nil || len(repositories) != 1 || repositories[0].Name != "fleet" {
		t.Fatalf("repositories=%+v error=%v", repositories, err)
	}
	status, err := db.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.GlobalSequence != first.Sequence {
		t.Fatal("failed command mutated event history")
	}
	for _, method := range []string{MethodRegisterRepository, MethodListRepositories} {
		if agentMethodAllowed(method) {
			t.Fatalf("restricted agent endpoint permits %s", method)
		}
	}
}

func TestRepositoryNamesAreUniqueUnderConcurrentRegistration(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		id, err := model.NewID()
		if err != nil {
			t.Fatal(err)
		}
		command := repositoryTestCommand(id, "same-name")
		wg.Add(1)
		go func() { defer wg.Done(); _, err := db.RegisterRepository(command); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful registrations=%d", successes)
	}
	repositories, err := db.ListRepositories()
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories=%+v error=%v", repositories, err)
	}
}

func TestMalformedRepositoryCommandsLeaveNoRecords(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, change := range []func(*RegisterRepositoryCommand){func(c *RegisterRepositoryCommand) { c.Repository.ID = "../escape" }, func(c *RegisterRepositoryCommand) { c.Repository.Name = "../escape" }, func(c *RegisterRepositoryCommand) { c.Repository.IntegrationBranch = "main.lock" }, func(c *RegisterRepositoryCommand) {
		c.Repository.Name = "--invalid-name"
	}, func(c *RegisterRepositoryCommand) { c.ProtocolVersion++ }, func(c *RegisterRepositoryCommand) { c.CreatedAt = time.Time{} }} {
		c := repositoryTestCommand(importTestID, "repo")
		change(&c)
		if _, err := db.RegisterRepository(c); err == nil {
			t.Fatalf("accepted malformed command: %+v", c)
		}
	}
	repositories, err := db.ListRepositories()
	if err != nil || len(repositories) != 0 {
		t.Fatalf("repositories=%+v error=%v", repositories, err)
	}
}
