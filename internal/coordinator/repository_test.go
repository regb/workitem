package coordinator

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"

	"github.com/regb/workitem/internal/testutil"
)

func repositoryTestCommand(id, name string) RegisterRepositoryCommand {
	now := time.Now().UTC()
	return RegisterRepositoryCommand{ID: "register:" + id, ProtocolVersion: ProtocolVersion, Repository: model.OwnedRepository{ID: id, Name: name, IntegrationBranch: "main", CreatedAt: now}, CreatedAt: now}
}

// Registration validates the owned Git store before recording it.
func TestRepositoryRegistrationRequiresItsGitStore(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(root, filepath.Join(testutil.ShortTempDir(t), "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := server.LocalClient()
	ctx := context.Background()
	command := repositoryTestCommand(importTestID, "repo")
	if _, err := client.RegisterRepository(ctx, command); err == nil {
		t.Fatal("registered missing Git store")
	}
	source := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial"}} {
		if out, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	path, err := model.OwnedRepositoryGitDir(root, importTestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitpkg.New("git").ImportBare(ctx, model.BareImportOptions{Source: source, Destination: path, IntegrationBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if result, err := client.RegisterRepository(ctx, command); err != nil || result.Repository.ID != importTestID {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if source, err := client.RepositorySource(ctx, "repo", ""); err != nil || source.GitDir != path || source.Commit == "" {
		t.Fatalf("source=%+v error=%v", source, err)
	}
}

func TestRepositoryRegistrationIsAtomicAndIdempotent(t *testing.T) {
	db, _ := itemDatabase(t)
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
}

func TestRepositoryNamesAreUniqueUnderConcurrentRegistration(t *testing.T) {
	db, _ := itemDatabase(t)
	oneWinner(t, 8, func(int) error {
		id, err := model.NewID()
		if err != nil {
			return err
		}
		_, err = db.RegisterRepository(repositoryTestCommand(id, "same-name"))
		return err
	})
	repositories, err := db.ListRepositories()
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories=%+v error=%v", repositories, err)
	}
}

func TestMalformedRepositoryCommandsLeaveNoRecords(t *testing.T) {
	db, _ := itemDatabase(t)
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
