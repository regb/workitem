package cli

import (
	"context"
	"encoding/json"
	"github.com/regb/workitem/internal/coordinator"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryOnboardingInfersIntegrationDestination(t *testing.T) {
	cfg, origin, out, _ := remoteCLI(t)
	ctx := context.Background()
	source := t.TempDir()
	remoteTestGit(t, source, "init", "-b", "main")
	remoteTestGit(t, source, "config", "user.name", "Test")
	remoteTestGit(t, source, "config", "user.email", "test@example.com")
	remoteTestCommit(t, source, "base", "base", "base")
	cases := []struct {
		name   string
		source string
		flags  []string
		url    string
		branch string
	}{
		{"remote", "file://localhost" + origin, []string{"--branch", "main"}, "file://localhost" + origin, "main"},
		{"local", origin, nil, origin, "main"},
		{"untracked", source, nil, "", ""},
		{"offline", origin, []string{"--local"}, "", ""},
		{"override", source, []string{"--upstream", origin, "--upstream-branch", "release/stable"}, origin, "release/stable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out.Reset()
			args := append([]string{"add", c.name, "--from", c.source}, c.flags...)
			if err := runRepository(ctx, args, cfg, true); err != nil {
				t.Fatal(err)
			}
			result, err := cfg.Coordinator.RepositoryViewSource(ctx, c.name, "")
			if err != nil {
				t.Fatal(err)
			}
			u := result.Repository.Upstream
			if c.url == "" {
				if u != nil {
					t.Fatalf("unexpected upstream=%+v", u)
				}
			} else if u == nil || u.URL != c.url || u.Branch != c.branch {
				t.Fatalf("upstream=%+v", u)
			}
			if strings.Contains(out.String(), "origin_url") || strings.Contains(out.String(), `"source"`) {
				t.Fatalf("onboarding provenance leaked into config: %s", out.String())
			}
			if paths := remoteTestGit(t, result.GitDir, "remote", "-v"); strings.Contains(paths, source) && c.url == "" {
				t.Fatalf("owned store retains original checkout: %s", paths)
			}
		})
	}
	// The local integration branch may follow a differently named origin branch.
	remoteTestGit(t, origin, "config", "branch.main.remote", "origin")
	remoteTestGit(t, origin, "config", "branch.main.merge", "refs/heads/trunk")
	if err := runRepository(ctx, []string{"add", "tracked", "--from", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	tracked, err := cfg.Coordinator.RepositoryViewSource(ctx, "tracked", "")
	if err != nil || tracked.Repository.Upstream == nil || tracked.Repository.Upstream.Branch != "trunk" {
		t.Fatalf("tracked=%+v err=%v", tracked, err)
	}
	// A relative local origin is resolved once, not relative to the owned store.
	nested := filepath.Join(t.TempDir(), "checkout")
	remoteTestGit(t, "", "clone", origin, nested)
	remoteTestGit(t, nested, "remote", "set-url", "origin", filepath.Join("..", "remote.git"))
	if err := runRepository(ctx, []string{"add", "relative", "--from", nested}, cfg, false); err != nil {
		t.Fatal(err)
	}
	relative, err := cfg.Coordinator.RepositoryViewSource(ctx, "relative", "")
	if err != nil || relative.Repository.Upstream.URL != filepath.Join(filepath.Dir(nested), "remote.git") {
		t.Fatalf("relative=%+v err=%v", relative, err)
	}
	// Onboarding never fetches that inferred URL, which may not yet be reachable.
	if _, err := os.Stat(relative.Repository.Upstream.URL); !os.IsNotExist(err) {
		t.Fatal("fixture should have an unavailable upstream")
	}
}

func TestRepositoryUpstreamJSONAndExplicitConfiguration(t *testing.T) {
	cfg, origin, out, _ := remoteCLI(t)
	ctx := context.Background()
	if err := runRepository(ctx, []string{"upstream", "project", origin, "--branch", "release/stable"}, cfg, true); err != nil {
		t.Fatal(err)
	}
	source, err := cfg.Coordinator.RepositoryViewSource(ctx, "project", "")
	if err != nil || source.Repository.Upstream.Branch != "release/stable" {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	out.Reset()
	if err := runRepository(ctx, []string{"list", "--all"}, cfg, true); err != nil {
		t.Fatal(err)
	}
	var repos []coordinator.RepositoryView
	if err := json.Unmarshal(out.Bytes(), &repos); err != nil || len(repos) != 1 || repos[0].Upstream.URL != origin {
		t.Fatalf("list=%s err=%v", out, err)
	}
	if strings.Contains(out.String(), "origin_url") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := runRepository(ctx, []string{"list"}, cfg, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "UPSTREAM") || !strings.Contains(out.String(), origin+" branch release/stable") {
		t.Fatal(out.String())
	}
	for _, args := range [][]string{{"add", "bad", "--from", origin, "--local", "--upstream", origin}, {"add", "bad", "--from", origin, "--local", "--upstream-branch", "main"}, {"upstream", "project", "--clear", "--branch", "main"}} {
		if err := runRepository(ctx, args, cfg, false); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
