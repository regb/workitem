package direnv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/app/contract"
	adapter "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/extension"
)

type lifecycleClient struct {
	status                    adapter.Status
	allowed, denied, executed int
	denyErr                   error
}

func (c *lifecycleClient) Status(context.Context, string) (adapter.Status, error) {
	return c.status, nil
}
func (c *lifecycleClient) Allow(context.Context, string) error { c.allowed++; return nil }
func (c *lifecycleClient) Deny(context.Context, string) error  { c.denied++; return c.denyErr }
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
func TestReusedSlotRevokesTrustEvenWhenLoadingDisabled(t *testing.T) {
	root, path := fixtureRC(t)
	client := &lifecycleClient{status: adapter.Status{Found: true, Allowed: true, RCPath: path}}
	p := extension.Pipeline{Hooks: []extension.Hooks{New(client, nil)}, Options: func(_ context.Context, _ string, _ string) (map[string]any, error) {
		return map[string]any{"load": false, "trust": "always"}, nil
	}}
	if _, err := p.PrepareCheckout(context.Background(), contract.CheckoutPreparation{Path: root, Reused: true, Generation: 2}); err != nil || client.denied != 1 {
		t.Fatal(client, err)
	}
	if _, _, _, err := p.Environment(context.Background(), "runtime", extension.EnvironmentRequest{Subject: extension.Subject{CheckoutPath: root}}); err != nil || client.executed != 0 || client.allowed != 0 {
		t.Fatal(client, err)
	}
	client.denyErr = errors.New("cannot revoke")
	if _, err := p.PrepareCheckout(context.Background(), contract.CheckoutPreparation{Path: root, Reused: true}); err == nil {
		t.Fatal("revocation failure ignored")
	}
}
func TestTrustPolicyCannotEscapeCheckoutOrApproveChangedContent(t *testing.T) {
	for _, kind := range []string{"ancestor", "symlink", "changed"} {
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
			approve := func(context.Context, extension.Approval) (bool, error) {
				if kind == "changed" {
					if err := os.WriteFile(path, []byte("changed shell code"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return true, nil
			}
			p := extension.Pipeline{Hooks: []extension.Hooks{New(client, approve)}}
			if _, _, _, err := p.Environment(context.Background(), "runtime", extension.EnvironmentRequest{Subject: extension.Subject{CheckoutPath: root}}); err == nil || client.allowed != 0 || client.executed != 0 {
				t.Fatal("unsafe approval", client, err)
			}
		})
	}
}
func TestPromptIsSkippedForExplicitNeverAndAlwaysPolicies(t *testing.T) {
	for _, trust := range []string{"never", "always"} {
		root, path := fixtureRC(t)
		client := &lifecycleClient{status: adapter.Status{Found: true, RCPath: path}}
		p := extension.Pipeline{Hooks: []extension.Hooks{New(client, func(context.Context, extension.Approval) (bool, error) {
			t.Fatal("unexpected prompt")
			return false, nil
		})}, Options: func(_ context.Context, _ string, _ string) (map[string]any, error) {
			return map[string]any{"trust": trust}, nil
		}}
		_, _, _, err := p.Environment(context.Background(), "runtime", extension.EnvironmentRequest{Subject: extension.Subject{CheckoutPath: root}})
		if err != nil {
			t.Fatal(err)
		}
		if trust == "never" && (client.executed != 0 || client.allowed != 0) {
			t.Fatal(client)
		}
		if trust == "always" && (client.executed != 1 || client.allowed != 1) {
			t.Fatal(client)
		}
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
	pipeline := extension.Pipeline{Hooks: []extension.Hooks{New(client, nil)}}
	if _, _, _, err := pipeline.Environment(context.Background(), "runtime", extension.EnvironmentRequest{Subject: extension.Subject{CheckoutPath: filepath.Join(alias, "checkout")}}); err != nil || client.executed != 1 {
		t.Fatal("canonical ancestor alias refused", err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := pipeline.Environment(context.Background(), "command", extension.EnvironmentRequest{Subject: extension.Subject{CheckoutPath: filepath.Join(alias, "checkout", "nested"), BoundaryPath: filepath.Join(alias, "checkout")}, AllowedKeys: []string{"WI_LIST_LABELS"}}); err != nil || client.executed != 2 {
		t.Fatal("enclosing approved environment refused", err)
	}
}

func TestInheritedCleanupRunsWithLoadingDisabled(t *testing.T) {
	p := extension.Pipeline{Hooks: []extension.Hooks{New(nil, nil)}, Options: func(_ context.Context, _ string, _ string) (map[string]any, error) {
		return map[string]any{"load": false}, nil
	}}
	env, removed, _, err := p.Environment(context.Background(), "sanitize", extension.EnvironmentRequest{Values: map[string]string{"DIRENV_DIFF": encodedDirenvDiff(t, `{"p":{"OLD_SECRET":null}}`), "OLD_SECRET": "secret", "HOME": "/home/operator", "PATH": "/bin"}})
	if err != nil || env["OLD_SECRET"] != "" || env["DIRENV_DIFF"] != "" || env["PATH"] != "/bin" || len(removed) != 2 {
		t.Fatal("inherited project state survived", err)
	}
}
