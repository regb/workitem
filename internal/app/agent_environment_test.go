package app

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
	direnvpkg "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/model"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

type runtimeDirenv struct {
	status     direnvpkg.Status
	env        map[string]string
	envCalls   int
	allowCalls int
	base       map[string]string
}

func (d *runtimeDirenv) Status(context.Context, string) (direnvpkg.Status, error) {
	return d.status, nil
}
func (d *runtimeDirenv) Allow(context.Context, string) error {
	d.allowCalls++
	return nil
}
func (*runtimeDirenv) Deny(context.Context, string) error { return nil }
func (d *runtimeDirenv) Environment(_ context.Context, _ string, base map[string]string) (map[string]string, error) {
	d.envCalls++
	d.base = base
	return d.env, nil
}

type fakeBaseTmux struct{ global map[string]string }

func (f fakeBaseTmux) GlobalEnvironment(context.Context) (map[string]string, error) {
	return f.global, nil
}
func (fakeBaseTmux) HasSession(context.Context, string) (bool, error) { return false, nil }
func (fakeBaseTmux) EnsureSession(context.Context, tmuxpkg.SessionSpec) (bool, error) {
	return false, nil
}
func (fakeBaseTmux) LaunchCommand(context.Context, tmuxpkg.LaunchSpec) error { return nil }
func (fakeBaseTmux) PaneInfo(context.Context, string) (tmuxpkg.PaneInfo, error) {
	return tmuxpkg.PaneInfo{}, nil
}
func (fakeBaseTmux) AttachOrSwitch(context.Context, string, bool) error { return nil }
func (fakeBaseTmux) KillSession(context.Context, string) error          { return nil }
func (fakeBaseTmux) KillSessionAsync(context.Context, string) error     { return nil }
func (fakeBaseTmux) CurrentSession(context.Context) (string, error)     { return "", nil }
func (fakeBaseTmux) SessionEnvironment(context.Context, string) (map[string]string, error) {
	return nil, nil
}

func encodeTestDirenvDiff(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write([]byte(`{"p":{"PATH":"/x"},"n":{"PROJECT_GH_TOKEN":"","ISSUE_TRACKER_TOKEN":""}}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.URLEncoding.EncodeToString(buf.Bytes())
}

func TestAgentRuntimeEnvironmentLoadsOnlyTrustedEnvrc(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: true, RCPath: path + "/.envrc"}, env: map[string]string{"TOKEN": "value"}}
	a := &App{Direnv: d}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), model.Manifest{Checkout: model.Checkout{Path: &path}})
	if err != nil || env["TOKEN"] != "value" || len(warnings) != 0 || d.envCalls != 1 {
		t.Fatalf("env=%v warnings=%v calls=%d", env, warnings, d.envCalls)
	}
}
func TestAgentRuntimeEnvironmentPromptsBeforeAllowingUntrustedEnvrc(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, RCPath: path + "/.envrc"}, env: map[string]string{"TOKEN": "value"}}
	a := &App{Direnv: d, ApproveDirenv: func(context.Context, model.Manifest, string) (bool, error) { return true, nil }}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), model.Manifest{Checkout: model.Checkout{Path: &path}})
	if err != nil || env["TOKEN"] != "value" || d.allowCalls != 1 || d.envCalls != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "operator approval") {
		t.Fatalf("env=%v warnings=%v allow=%d environment=%d", env, warnings, d.allowCalls, d.envCalls)
	}
}

func TestAgentRuntimeEnvironmentAutoTrustsOnlyConfiguredRepository(t *testing.T) {
	repository := t.TempDir()
	checkout := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, RCPath: checkout + "/.envrc"}, env: map[string]string{"TOKEN": "value"}}
	a := &App{Direnv: d, DirenvConfig: config.DirenvConfig{AutoTrustRepositories: []string{repository}}}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), model.Manifest{Repository: model.Repository{RootAtCreation: repository}, Checkout: model.Checkout{Path: &checkout}})
	if err != nil || env["TOKEN"] != "value" || d.allowCalls != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "user config") {
		t.Fatalf("env=%v warnings=%v allow=%d", env, warnings, d.allowCalls)
	}
}

func TestAgentRuntimeEnvironmentIgnoresReservedControlVariables(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: true, RCPath: path + "/.envrc"}, env: map[string]string{"WI_ID": "wrong", "PI_CODING_AGENT_SESSION_DIR": "/wrong", "WI_LIST_LABELS": "+project", "WI_ITEM_DEFAULT_LABELS": "project", "SAFE": "value", "TMUX": "/stale", "TMUX_PANE": "%9", "DIRENV_DIFF": "internal"}}
	a := &App{Direnv: d}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), model.Manifest{Checkout: model.Checkout{Path: &path}})
	if err != nil || env["SAFE"] != "value" || env["WI_LIST_LABELS"] != "+project" || env["WI_ITEM_DEFAULT_LABELS"] != "project" || env["WI_ID"] != "" || env["PI_CODING_AGENT_SESSION_DIR"] != "" || env["TMUX"] != "" || env["TMUX_PANE"] != "" || env["DIRENV_DIFF"] != "" || len(warnings) != 2 {
		t.Fatalf("env=%v warnings=%v err=%v", env, warnings, err)
	}
}

func TestAgentRuntimeEnvironmentUsesGlobalTmuxEnvironmentAsBase(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: true, RCPath: path + "/.envrc"}, env: map[string]string{"TOKEN": "value"}}
	tmuxClient := fakeBaseTmux{global: map[string]string{"PATH": "/clean/bin", "HOME": "/home/user"}}
	a := &App{Direnv: d, Tmux: tmuxClient}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), model.Manifest{Checkout: model.Checkout{Path: &path}})
	if err != nil || env["TOKEN"] != "value" || len(warnings) != 0 || d.base["PATH"] != "/clean/bin" || d.base["HOME"] != "/home/user" {
		t.Fatalf("env=%v warnings=%v base=%v err=%v", env, warnings, d.base, err)
	}
}

func TestDirenvBaseEnvironmentScrubsServerAndPanePollution(t *testing.T) {
	global := map[string]string{
		"PATH":                        "/clean/bin",
		"HOME":                        "/home/user",
		"DISPLAY":                     ":0",
		"PROJECT_GH_TOKEN":            "secret",
		"ISSUE_TRACKER_TOKEN":         "tracker-secret",
		"DIRENV_DIFF":                 encodeTestDirenvDiff(t),
		"DIRENV_DIR":                  "/stale",
		"WI_ID":                       "stale-item",
		"PI_CODING_AGENT_SESSION_DIR": "/stale/pi",
	}
	tmuxClient := fakeBaseTmux{global: global}
	a := &App{Tmux: tmuxClient}
	t.Setenv("DIRENV_DIFF", "")
	base, scrub := a.direnvBaseEnvironment(context.Background())
	if base["PROJECT_GH_TOKEN"] != "" || base["ISSUE_TRACKER_TOKEN"] != "" || base["WI_ID"] != "" || base["PI_CODING_AGENT_SESSION_DIR"] != "" || base["DIRENV_DIR"] != "" {
		t.Fatalf("base still contains pollution: %v", base)
	}
	if base["PATH"] != "/clean/bin" || base["HOME"] != "/home/user" || base["DISPLAY"] != ":0" {
		t.Fatalf("base lost essential variables: %v", base)
	}
	scrubSet := map[string]bool{}
	for _, key := range scrub {
		scrubSet[key] = true
	}
	for _, key := range []string{"PROJECT_GH_TOKEN", "ISSUE_TRACKER_TOKEN", "WI_ID", "PI_CODING_AGENT_SESSION_DIR", "DIRENV_DIR"} {
		if !scrubSet[key] {
			t.Fatalf("scrub list missing %q: %v", key, scrub)
		}
	}
}

func TestAgentRuntimeEnvironmentWarnsWithoutEvaluatingUntrustedEnvrc(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: false, RCPath: path + "/.envrc"}}
	a := &App{Direnv: d}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), model.Manifest{Checkout: model.Checkout{Path: &path}})
	if err != nil || len(env) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "not trusted") || d.envCalls != 0 {
		t.Fatalf("env=%v warnings=%v calls=%d", env, warnings, d.envCalls)
	}
}
