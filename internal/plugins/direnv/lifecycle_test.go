package direnv

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	adapter "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/plugin"
)

type lifecycleClient struct {
	status            adapter.Status
	allowed, executed int
	allowedWith       map[string]string
}

func (c *lifecycleClient) Status(context.Context, string) (adapter.Status, error) {
	return c.status, nil
}
func (c *lifecycleClient) Allow(_ context.Context, _ string, env map[string]string) error {
	c.allowed++
	c.allowedWith = env
	return nil
}
func (c *lifecycleClient) Environment(context.Context, string, map[string]string) (map[string]string, error) {
	c.executed++
	return map[string]string{"VALUE": "target"}, nil
}
func fixtureRC(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, ".envrc")
	if err := os.WriteFile(path, []byte("export VALUE=target"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, path
}
func TestWorktreeEnvrcIsAllowedAndLoadedWithoutAsking(t *testing.T) {
	root, path := fixtureRC(t)
	client := &lifecycleClient{status: adapter.Status{Found: true, RCPath: path}}
	p := plugin.Pipeline{Hooks: []plugin.Hooks{New(client)}}
	env, _, _, err := p.Environment(context.Background(), "runtime", plugin.EnvironmentRequest{Subject: plugin.Subject{CheckoutPath: root}, Values: map[string]string{"HOME": "/home/agent"}})
	if err != nil || client.allowed != 1 || client.executed != 1 || env["VALUE"] != "target" {
		t.Fatal(client, env, err)
	}
	// direnv keeps allowances where the environment it runs with says.
	if client.allowedWith["HOME"] != "/home/agent" {
		t.Fatalf("allowed with %v", client.allowedWith)
	}
	// An allowed file is loaded without allowing it again.
	client.status.Allowed = true
	if _, _, _, err := p.Environment(context.Background(), "runtime", plugin.EnvironmentRequest{Subject: plugin.Subject{CheckoutPath: root}}); err != nil || client.allowed != 1 || client.executed != 2 {
		t.Fatal(client, err)
	}
}
func TestLoadingDisabledNeitherAllowsNorRuns(t *testing.T) {
	root, path := fixtureRC(t)
	client := &lifecycleClient{status: adapter.Status{Found: true, RCPath: path}}
	p := plugin.Pipeline{Hooks: []plugin.Hooks{New(client)}, Options: func(_ context.Context, _ string, _ string) (map[string]any, error) {
		return map[string]any{"load": false}, nil
	}}
	if _, _, _, err := p.Environment(context.Background(), "runtime", plugin.EnvironmentRequest{Subject: plugin.Subject{CheckoutPath: root}}); err != nil || client.allowed != 0 || client.executed != 0 {
		t.Fatal(client, err)
	}
}
func TestEnvrcOutsideTheCheckoutIsRefused(t *testing.T) {
	for _, kind := range []string{"ancestor", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, path := fixtureRC(t)
			client := &lifecycleClient{status: adapter.Status{Found: true, RCPath: path}}
			if kind == "ancestor" {
				_, other := fixtureRC(t)
				client.status.RCPath = other
			}
			if kind == "symlink" {
				_, other := fixtureRC(t)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			}
			p := plugin.Pipeline{Hooks: []plugin.Hooks{New(client)}}
			if _, _, _, err := p.Environment(context.Background(), "runtime", plugin.EnvironmentRequest{Subject: plugin.Subject{CheckoutPath: root}}); err == nil || client.allowed != 0 || client.executed != 0 {
				t.Fatal("unsafe allowance", client, err)
			}
		})
	}
}
func TestCanonicalAncestorAliasesDoNotChangeApprovalIdentity(t *testing.T) {
	parent := t.TempDir()
	actual := filepath.Join(parent, "actual")
	root := filepath.Join(actual, "checkout")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".envrc")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	client := &lifecycleClient{status: adapter.Status{Found: true, Allowed: true, RCPath: path}}
	pipeline := plugin.Pipeline{Hooks: []plugin.Hooks{New(client)}}
	if _, _, _, err := pipeline.Environment(context.Background(), "runtime", plugin.EnvironmentRequest{Subject: plugin.Subject{CheckoutPath: filepath.Join(alias, "checkout")}}); err != nil || client.executed != 1 {
		t.Fatal("canonical ancestor alias refused", err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := pipeline.Environment(context.Background(), "command", plugin.EnvironmentRequest{Subject: plugin.Subject{CheckoutPath: filepath.Join(alias, "checkout", "nested"), BoundaryPath: filepath.Join(alias, "checkout")}, AllowedKeys: []string{"WI_LIST_LABELS"}}); err != nil || client.executed != 2 {
		t.Fatal("enclosing approved environment refused", err)
	}
}

func TestInheritedCleanupRunsWithLoadingDisabled(t *testing.T) {
	p := plugin.Pipeline{Hooks: []plugin.Hooks{New(nil)}, Options: func(_ context.Context, _ string, _ string) (map[string]any, error) {
		return map[string]any{"load": false}, nil
	}}
	env, removed, _, err := p.Environment(context.Background(), "sanitize", plugin.EnvironmentRequest{Values: map[string]string{"DIRENV_DIFF": encodedDirenvDiff(t, `{"p":{"OLD_SECRET":null}}`), "OLD_SECRET": "secret", "HOME": "/home/operator", "PATH": "/bin"}})
	if err != nil || env["OLD_SECRET"] != "" || env["DIRENV_DIFF"] != "" || env["PATH"] != "/bin" || len(removed) != 2 {
		t.Fatal("inherited project state survived", err)
	}
}
