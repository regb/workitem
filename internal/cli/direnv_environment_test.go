package cli

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"testing"

	"github.com/regb/workitem/internal/model"
)

type fakeCLIDirenv struct {
	status      model.DirenvStatus
	environment map[string]string
	base        map[string]string
	calls       int
}

func (f *fakeCLIDirenv) Status(context.Context, string) (model.DirenvStatus, error) {
	return f.status, nil
}
func (f *fakeCLIDirenv) Environment(_ context.Context, _ string, base map[string]string) (map[string]string, error) {
	f.calls++
	f.base = cloneEnvironment(base)
	return cloneEnvironment(f.environment), nil
}

func TestLoadCLIProjectEnvironmentImportsAllowedWiSettings(t *testing.T) {
	client := &fakeCLIDirenv{status: model.DirenvStatus{Found: true, Allowed: true}, environment: map[string]string{
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
	client := &fakeCLIDirenv{status: model.DirenvStatus{Found: true, Allowed: true}, environment: map[string]string{"WI_LIST_LABELS": "+project"}}
	got, err := loadCLIProjectEnvironment(context.Background(), client, "/repo", map[string]string{"WI_LIST_LABELS": "+explicit"})
	if err != nil || got["WI_LIST_LABELS"] != "+explicit" {
		t.Fatalf("environment=%+v err=%v", got, err)
	}
}

func TestLoadCLIProjectEnvironmentReplacesStaleDirenvValue(t *testing.T) {
	diff := encodedDirenvDiff(t, `{"p":{"WI_LIST_LABELS":null,"SECRET":null}}`)
	client := &fakeCLIDirenv{status: model.DirenvStatus{Found: true, Allowed: true}, environment: map[string]string{"WI_LIST_LABELS": "+current"}}
	got, err := loadCLIProjectEnvironment(context.Background(), client, "/repo", map[string]string{"DIRENV_DIFF": diff, "WI_LIST_LABELS": "+stale", "SECRET": "old"})
	if err != nil || got["WI_LIST_LABELS"] != "+current" {
		t.Fatalf("environment=%+v err=%v", got, err)
	}
	if _, exists := client.base["SECRET"]; exists {
		t.Fatalf("stale managed variable passed to direnv: %+v", client.base)
	}
}

func TestLoadCLIProjectEnvironmentDoesNotRunUntrustedEnvrc(t *testing.T) {
	client := &fakeCLIDirenv{status: model.DirenvStatus{Found: true, Allowed: false}, environment: map[string]string{"WI_LIST_LABELS": "+project"}}
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
