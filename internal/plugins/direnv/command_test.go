package direnv

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"testing"

	adapter "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/plugin"
	"maps"
	"os"
	"path/filepath"
)

type fakeCLIDirenv struct {
	status      adapter.Status
	environment map[string]string
	base        map[string]string
	calls       int
}

func (f *fakeCLIDirenv) Status(context.Context, string) (adapter.Status, error) {
	return f.status, nil
}
func (f *fakeCLIDirenv) Environment(_ context.Context, _ string, base map[string]string) (map[string]string, error) {
	f.calls++
	f.base = cloneEnvironment(base)
	return cloneEnvironment(f.environment), nil
}

func TestLoadCLIProjectEnvironmentImportsAllowedWiSettings(t *testing.T) {
	client := &fakeCLIDirenv{status: adapter.Status{Found: true, Allowed: true}, environment: map[string]string{
		"WI_LIST_LABELS": "+project", "WI_ITEM_DEFAULT_LABELS": "backend", "PATH": "/project/bin", "WI_ID": "wrong",
	}}
	got, err := loadCLIProjectEnvironment(context.Background(), client, "/repo", map[string]string{"PATH": "/usr/bin", "WI_ID": "item-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got["WI_LIST_LABELS"] != "+project" || got["WI_ITEM_DEFAULT_LABELS"] != "backend" || got["PATH"] != "/usr/bin" || got["WI_ID"] != "item-1" {
		t.Fatalf("environment=%+v", got)
	}
}

func TestLoadCLIProjectEnvironmentPreservesExplicitCallerValue(t *testing.T) {
	client := &fakeCLIDirenv{status: adapter.Status{Found: true, Allowed: true}, environment: map[string]string{"WI_LIST_LABELS": "+project"}}
	got, err := loadCLIProjectEnvironment(context.Background(), client, "/repo", map[string]string{"WI_LIST_LABELS": "+explicit"})
	if err != nil || got["WI_LIST_LABELS"] != "+explicit" {
		t.Fatalf("environment=%+v err=%v", got, err)
	}
}

func TestLoadCLIProjectEnvironmentReplacesStaleDirenvValue(t *testing.T) {
	diff := encodedDirenvDiff(t, `{"p":{"WI_LIST_LABELS":null,"SECRET":null,"PATH":null}}`)
	client := &fakeCLIDirenv{status: adapter.Status{Found: true, Allowed: true}, environment: map[string]string{"WI_LIST_LABELS": "+current"}}
	got, err := loadCLIProjectEnvironment(context.Background(), client, "/repo", map[string]string{"DIRENV_DIFF": diff, "WI_LIST_LABELS": "+stale", "SECRET": "old", "PATH": "/usr/bin"})
	if err != nil || got["WI_LIST_LABELS"] != "+current" {
		t.Fatalf("environment=%+v err=%v", got, err)
	}
	if _, exists := client.base["SECRET"]; exists {
		t.Fatalf("stale managed variable passed to direnv: %+v", client.base)
	}
	if client.base["PATH"] != "/usr/bin" {
		t.Fatalf("PATH was not preserved for direnv: %+v", client.base)
	}
}

func TestLoadCLIProjectEnvironmentDoesNotRunUntrustedEnvrc(t *testing.T) {
	client := &fakeCLIDirenv{status: adapter.Status{Found: true, Allowed: false}, environment: map[string]string{"WI_LIST_LABELS": "+project"}}
	got, err := loadCLIProjectEnvironment(context.Background(), client, "/repo", map[string]string{"PATH": "/usr/bin"})
	if err != nil || client.calls != 0 || got["PATH"] != "/usr/bin" {
		t.Fatalf("environment=%+v calls=%d err=%v", got, client.calls, err)
	}
}

func encodedDirenvDiff(t *testing.T, value string) string {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.URLEncoding.EncodeToString(compressed.Bytes())
}

func (f *fakeCLIDirenv) Allow(context.Context, string) error { return nil }
func (f *fakeCLIDirenv) Deny(context.Context, string) error  { return nil }
func cloneEnvironment(v map[string]string) map[string]string { return maps.Clone(v) }
func loadCLIProjectEnvironment(ctx context.Context, client *fakeCLIDirenv, _ string, source map[string]string) (map[string]string, error) {
	root, err := os.MkdirTemp("", "wi-direnv-command-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	if client.status.Found {
		client.status.RCPath = filepath.Join(root, ".envrc")
		if err := os.WriteFile(client.status.RCPath, []byte("fixture"), 0600); err != nil {
			return nil, err
		}
	}
	p := plugin.Pipeline{Hooks: []plugin.Hooks{New(client, nil)}}
	result, _, _, err := p.Environment(ctx, "command", plugin.EnvironmentRequest{Subject: plugin.Subject{CheckoutPath: root}, Values: source, Source: source, AllowedKeys: []string{"WI_LIST_LABELS", "WI_ITEM_DEFAULT_LABELS"}})
	return result, err
}
