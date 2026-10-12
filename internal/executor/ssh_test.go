package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/executor/executortest"
)

func TestSSHProfileValidation(t *testing.T) {
	good := Profile{Driver: "ssh", Host: "builder", Root: "/home/dev/wi"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Profile{
		{Driver: "ssh", Host: "-bad", Root: "/srv/wi"},
		{Driver: "ssh", Host: "builder", Root: "/"},
		{Driver: "ssh", Host: "builder", Root: "relative"},
		{Driver: "ssh", Host: "builder", Root: "/srv/../wi"},
		{Driver: "ssh", Host: "builder", Root: "/srv/wi", Image: "dev"},
		{Driver: "ssh", Host: "builder", Root: "/srv/wi", Network: "bridge"},
		{Driver: "ssh", Host: "builder", Root: "/srv/wi", SSHConfig: "relative"},
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}

func TestSSHQuotesRemoteArgumentsAndDisablesForwarding(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ssh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output bytes.Buffer
	err := (SSH{Host: "builder"}).Run(context.Background(), []string{"sh", "-c", "printf '%s' \"$1\"", "wi", "a'b; $(touch hacked)"}, nil, &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oForwardAgent=no", "-oClearAllForwardings=yes", "-T\n--\nbuilder\n", "'a'\"'\"'b; $(touch hacked)'"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %q", want, output.String())
		}
	}
}

func TestSSHTerminal(t *testing.T) {
	s := executortest.SSHServer(t)
	t.Setenv("TERM", "xterm-256color")
	c := Client{Profile: Profile{Driver: "ssh", Host: s.Host, Root: s.Root, SSHConfig: s.Config}, Identity: Identity{Name: "terminal", Owner: "test", Claim: "terminal", Generation: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := c.Attach(ctx, strings.NewReader("printf 'SSH_%s\\n' TTY_READY; exit\n"), &out, &stderr); err != nil {
		t.Fatalf("attach: %v stdout=%s stderr=%s", err, out.String(), stderr.String())
	}
	if !strings.Contains(out.String(), "SSH_TTY_READY") {
		t.Fatalf("shell did not run input: %s", out.String())
	}
	if err := c.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSSHReleaseScopeAndMissingWorkspace(t *testing.T) {
	s := executortest.SSHServer(t)
	profile := Profile{Driver: "ssh", Host: s.Host, Root: "/home/wi/executor'es", SSHConfig: s.Config}
	c := Client{Profile: profile, Identity: Identity{Name: "scope", Owner: "test", Claim: "scope", Generation: 1}}
	ctx := context.Background()
	ssh := SSH{Host: s.Host, Config: s.Config}
	var out, stderr bytes.Buffer
	if err := ssh.Run(ctx, []string{"touch", "/home/wi/keep-me"}, nil, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := c.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	stale := c
	stale.Identity.Claim = "other"
	if err := stale.Destroy(ctx); err == nil {
		t.Fatal("destroy accepted another claim")
	}
	if err := c.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if exists, err := c.Exists(ctx); err != nil || exists {
		t.Fatalf("workspace not removed %t %v", exists, err)
	}
	if err := ssh.Run(ctx, []string{"test", "-f", "/home/wi/keep-me"}, nil, &out, &stderr); err != nil {
		t.Fatal("release touched unrelated remote data", err)
	}
	if err := c.Start(ctx); err == nil {
		t.Fatal("missing workspace was silently recreated")
	}
}
