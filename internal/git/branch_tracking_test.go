package git_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
)

func trackingFixture(t *testing.T) (gitpkg.Client, string, string, string) {
	t.Helper()
	source := setupRepo(t)
	branch := runGitOutput(t, source, "branch", "--show-current")
	store := filepath.Join(t.TempDir(), "owned.git")
	g := gitpkg.New("git")
	if err := g.ImportBare(context.Background(), model.BareImportOptions{Source: source, Destination: store, IntegrationBranch: branch, OriginURL: source, UpstreamBranch: branch}); err != nil {
		t.Fatal(err)
	}
	return g, store, source, branch
}

func TestGitOwnsBranchTrackingAndClearPreservesOrigin(t *testing.T) {
	g, store, source, branch := trackingFixture(t)
	ctx := context.Background()
	old := runGitOutput(t, store, "rev-parse", branch)
	up, err := g.BranchUpstream(ctx, store, branch)
	if err != nil || up == nil || up.URL != source || up.Remote != "origin" || up.Branch != branch {
		t.Fatalf("up=%+v err=%v", up, err)
	}
	runGit(t, store, "config", "custom.keep", "yes")
	runGit(t, store, "remote", "set-url", "origin", "https://example.com/new.git")
	changed, err := g.BranchUpstream(ctx, store, branch)
	if err != nil || changed.URL == up.URL {
		t.Fatalf("changed=%+v err=%v", changed, err)
	}
	if err := g.SetBranchUpstream(ctx, store, branch, up, nil); err == nil {
		t.Fatal("stale edit accepted")
	}
	if err := g.SetBranchUpstream(ctx, store, branch, changed, nil); err != nil {
		t.Fatal(err)
	}
	if up, err := g.BranchUpstream(ctx, store, branch); err != nil || up != nil {
		t.Fatalf("clear=%+v err=%v", up, err)
	}
	if runGitOutput(t, store, "remote", "get-url", "origin") != "https://example.com/new.git" || runGitOutput(t, store, "config", "custom.keep") != "yes" || runGitOutput(t, store, "rev-parse", branch) != old {
		t.Fatal("tracking edit altered unrelated configuration or branch")
	}
	next, _ := model.ParseRepositoryUpstream(source, "release/stable")
	if err := g.SetBranchUpstream(ctx, store, branch, nil, next); err != nil {
		t.Fatal(err)
	}
	observed, err := g.BranchUpstream(ctx, store, branch)
	if err != nil || !reflect.DeepEqual(observed, next) {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	if _, err := os.Stat(filepath.Join(store, "config.lock")); !os.IsNotExist(err) {
		t.Fatal("configuration lock leaked")
	}
}

func TestGitTrackingIncludeIsObservedButNotOverwritten(t *testing.T) {
	g, store, source, branch := trackingFixture(t)
	ctx := context.Background()
	include := filepath.Join(store, "included.config")
	contents := "[remote \"origin\"]\n url = " + source + "\n"
	if err := os.WriteFile(include, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, store, "config", "--unset", "remote.origin.url")
	runGit(t, store, "config", "include.path", include)
	previous, err := g.BranchUpstream(ctx, store, branch)
	if err != nil || previous.URL != source {
		t.Fatalf("up=%+v err=%v", previous, err)
	}
	before, _ := os.ReadFile(filepath.Join(store, "config"))
	next, _ := model.ParseRepositoryUpstream(source, "other")
	if err := g.SetBranchUpstream(ctx, store, branch, previous, next); err == nil || !strings.Contains(err.Error(), "include") {
		t.Fatalf("err=%v", err)
	}
	after, _ := os.ReadFile(filepath.Join(store, "config"))
	included, _ := os.ReadFile(include)
	if string(before) != string(after) || string(included) != contents {
		t.Fatal("included or local config changed")
	}
}

func TestGitTrackingRefspecAndMalformedConfiguration(t *testing.T) {
	g, store, _, branch := trackingFixture(t)
	ctx := context.Background()
	runGit(t, store, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/custom/*")
	up, err := g.BranchUpstream(ctx, store, branch)
	if err != nil || up.Ref() != "refs/remotes/custom/"+branch {
		t.Fatalf("up=%+v err=%v", up, err)
	}
	runGit(t, store, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
	if _, err := g.BranchUpstream(ctx, store, branch); err == nil {
		t.Fatal("fetch directly into local heads accepted")
	}
	runGit(t, store, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	runGit(t, store, "config", "--add", "branch."+branch+".merge", "refs/heads/other")
	if _, err := g.BranchUpstream(ctx, store, branch); err == nil {
		t.Fatal("multiple tracking branches accepted")
	}
}

func TestGitTrackingConfigLocksAndSymlinksPreserveFiles(t *testing.T) {
	g, store, source, branch := trackingFixture(t)
	ctx := context.Background()
	previous, _ := g.BranchUpstream(ctx, store, branch)
	next, _ := model.ParseRepositoryUpstream(source, "other")
	lock := filepath.Join(store, "config.lock")
	if err := os.WriteFile(lock, []byte("operator lock"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.SetBranchUpstream(ctx, store, branch, previous, next); err == nil {
		t.Fatal("operator lock ignored")
	}
	if data, _ := os.ReadFile(lock); string(data) != "operator lock" {
		t.Fatal("operator lock changed")
	}
	os.Remove(lock)
	config := filepath.Join(store, "config")
	outside := filepath.Join(t.TempDir(), "config")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, data, 0600); err != nil {
		t.Fatal(err)
	}
	os.Remove(config)
	if err := os.Symlink(outside, config); err != nil {
		t.Fatal(err)
	}
	if err := g.SetBranchUpstream(ctx, store, branch, previous, next); err == nil {
		t.Fatal("symlink configuration accepted")
	}
	if after, _ := os.ReadFile(outside); string(after) != string(data) {
		t.Fatal("external config changed")
	}
}

func TestGitTrackingConcurrentRawConfigEditIsNotOverwritten(t *testing.T) {
	g, store, source, branch := trackingFixture(t)
	ctx := context.Background()
	previous, _ := g.BranchUpstream(ctx, store, branch)
	next, _ := model.ParseRepositoryUpstream(source, "other")
	// Intercept a staging edit, simulating an editor which ignores config.lock.
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := "#!/usr/bin/env bash\nset -eu\nfor arg in \"$@\"; do if [ \"$arg\" = --replace-all ]; then printf '\\n[external]\\n keep = yes\\n' >> \"$2/config\"; break; fi; done\nexec git \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	g = gitpkg.New(wrapper)
	if err := g.SetBranchUpstream(ctx, store, branch, previous, next); err == nil || !strings.Contains(err.Error(), "concurrently") {
		t.Fatalf("err=%v", err)
	}
	if runGitOutput(t, store, "config", "external.keep") != "yes" {
		t.Fatal("external edit overwritten")
	}
	up, err := gitpkg.New("git").BranchUpstream(ctx, store, branch)
	if err != nil || !reflect.DeepEqual(up, previous) {
		t.Fatalf("up=%+v err=%v", up, err)
	}
}
