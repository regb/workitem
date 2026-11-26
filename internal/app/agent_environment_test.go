package app

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/agent"
	direnvpkg "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
	direnvext "github.com/regb/workitem/internal/plugins/direnv"
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
func (d *runtimeDirenv) Allow(context.Context, string, map[string]string) error {
	d.allowCalls++
	return nil
}
func (d *runtimeDirenv) Environment(_ context.Context, _ string, base map[string]string) (map[string]string, error) {
	d.envCalls++
	d.base = base
	return d.env, nil
}

type fakeBaseTmux struct {
	global  map[string]string
	session map[string]string
}

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
func (fakeBaseTmux) KillSession(context.Context, string) error      { return nil }
func (fakeBaseTmux) CurrentSession(context.Context) (string, error) { return "", nil }
func (f fakeBaseTmux) SessionEnvironment(context.Context, string) (map[string]string, error) {
	return f.session, nil
}

func withRepository(m model.Manifest, id string) model.Manifest {
	m.Repository.ID = id
	return m
}

// claimedManifest is an item whose checkout is claimed at path.
func claimedManifest(path string) model.Manifest {
	return model.Manifest{Checkout: model.Checkout{ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: path}}}
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

func runtimeHooks(t *testing.T, d *runtimeDirenv) plugin.Pipeline {
	t.Helper()
	if d == nil {
		return plugin.Pipeline{Hooks: []plugin.Hooks{direnvext.New(nil)}}
	}
	if d != nil && d.status.Found {
		if err := os.WriteFile(d.status.RCPath, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return plugin.Pipeline{Hooks: []plugin.Hooks{direnvext.New(d)}}
}

func TestAgentRuntimeEnvironmentLoadsAnAllowedEnvrc(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: true, RCPath: path + "/.envrc"}, env: map[string]string{"TOKEN": "value"}}
	a := &App{Preparation: runtimeHooks(t, d)}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), claimedManifest(path), agent.ModeTUI)
	if err != nil || env["TOKEN"] != "value" || len(warnings) != 0 || d.envCalls != 1 {
		t.Fatalf("env=%v warnings=%v calls=%d", env, warnings, d.envCalls)
	}
}
func TestAgentRuntimeEnvironmentAllowsAndLoadsAWorktreeEnvrc(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, RCPath: path + "/.envrc"}, env: map[string]string{"TOKEN": "value"}}
	a := &App{Preparation: runtimeHooks(t, d)}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), claimedManifest(path), agent.ModeTUI)
	if err != nil || env["TOKEN"] != "value" || d.allowCalls != 1 || d.envCalls != 1 || len(warnings) != 0 {
		t.Fatalf("env=%v warnings=%v allow=%d environment=%d err=%v", env, warnings, d.allowCalls, d.envCalls, err)
	}
}

func TestAgentRuntimeEnvironmentIgnoresReservedControlVariables(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: true, RCPath: path + "/.envrc"}, env: map[string]string{"WI_ID": "wrong", "WI_AGENT_CONVERSATION_DIR": "/wrong", "WI_LIST_LABELS": "+project", "WI_ITEM_DEFAULT_LABELS": "project", "SAFE": "value", "TMUX": "/stale", "TMUX_PANE": "%9", "DIRENV_DIFF": "internal"}}
	a := &App{Preparation: runtimeHooks(t, d)}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), claimedManifest(path), agent.ModeTUI)
	if err != nil || env["SAFE"] != "value" || env["WI_LIST_LABELS"] != "+project" || env["WI_ITEM_DEFAULT_LABELS"] != "project" || env["WI_ID"] != "" || env["WI_AGENT_CONVERSATION_DIR"] != "" || env["TMUX"] != "" || env["TMUX_PANE"] != "" || env["DIRENV_DIFF"] != "" || len(warnings) != 2 {
		t.Fatalf("env=%v warnings=%v err=%v", env, warnings, err)
	}
}

func TestAgentRuntimeEnvironmentUsesGlobalTmuxEnvironmentAsBase(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: true, RCPath: path + "/.envrc"}, env: map[string]string{"TOKEN": "value"}}
	tmuxClient := fakeBaseTmux{global: map[string]string{"PATH": "/clean/bin", "HOME": "/home/user"}}
	a := &App{Preparation: runtimeHooks(t, d), Tmux: tmuxClient}
	env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), claimedManifest(path), agent.ModeTUI)
	if err != nil || env["TOKEN"] != "value" || len(warnings) != 0 || d.base["PATH"] != "/clean/bin" || d.base["HOME"] != "/home/user" {
		t.Fatalf("env=%v warnings=%v base=%v err=%v", env, warnings, d.base, err)
	}
}

func TestDirenvBaseEnvironmentScrubsServerAndPanePollution(t *testing.T) {
	global := map[string]string{
		"PATH":                      "/clean/bin",
		"HOME":                      "/home/user",
		"DISPLAY":                   ":0",
		"PROJECT_GH_TOKEN":          "secret",
		"ISSUE_TRACKER_TOKEN":       "tracker-secret",
		"DIRENV_DIFF":               encodeTestDirenvDiff(t),
		"DIRENV_DIR":                "/stale",
		"WI_ID":                     "stale-item",
		"WI_AGENT_CONVERSATION_DIR": "/stale/conversations",
	}
	tmuxClient := fakeBaseTmux{global: global}
	a := &App{Preparation: runtimeHooks(t, nil), Tmux: tmuxClient}
	t.Setenv("DIRENV_DIFF", "")
	base, scrub, _, err := a.baseRuntimeEnvironment(context.Background(), agent.ModeTUI, plugin.Subject{Mode: string(agent.ModeTUI)})
	if err != nil {
		t.Fatal(err)
	}
	if base["PROJECT_GH_TOKEN"] != "" || base["ISSUE_TRACKER_TOKEN"] != "" || base["WI_ID"] != "" || base["WI_AGENT_CONVERSATION_DIR"] != "" || base["DIRENV_DIR"] != "" {
		t.Fatalf("base still contains pollution: %v", base)
	}
	if base["PATH"] != "/clean/bin" || base["HOME"] != "/home/user" || base["DISPLAY"] != ":0" {
		t.Fatalf("base lost essential variables: %v", base)
	}
	scrubSet := map[string]bool{}
	for _, key := range scrub {
		scrubSet[key] = true
	}
	for _, key := range []string{"PROJECT_GH_TOKEN", "ISSUE_TRACKER_TOKEN", "WI_ID", "WI_AGENT_CONVERSATION_DIR", "DIRENV_DIR"} {
		if !scrubSet[key] {
			t.Fatalf("scrub list missing %q: %v", key, scrub)
		}
	}
}

func TestAgentRuntimeEnvironmentRemovesValuesLeftByEarlierLaunch(t *testing.T) {
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: true, RCPath: path + "/.envrc"}, env: map[string]string{"PI_CODING_AGENT_DIR": "/profiles/target"}}
	a := &App{Preparation: runtimeHooks(t, d), Tmux: fakeBaseTmux{global: map[string]string{"HOME": "/home/user", "PATH": "/bin"}, session: map[string]string{"OLD_PROJECT_TOKEN": "secret", "PI_CODING_AGENT_DIR": "/profiles/old"}}}
	m := claimedManifest(path)
	m.ID = "01KZYHGDCECSFS4BJ2SNTQP49V"
	env, scrub, _, err := a.agentRuntimeEnvironment(context.Background(), m, agent.ModeTUI)
	if err != nil {
		t.Fatal(err)
	}
	if env["PI_CODING_AGENT_DIR"] != "/profiles/target" {
		t.Fatalf("profile=%q", env["PI_CODING_AGENT_DIR"])
	}
	if !slices.Contains(scrub, "OLD_PROJECT_TOKEN") || slices.Contains(scrub, "PI_CODING_AGENT_DIR") {
		t.Fatalf("scrub=%v", scrub)
	}
}

func TestAgentRuntimeEnvironmentSkipsEnvrcWhereARepositoryTurnsLoadingOff(t *testing.T) {
	repository := "01KZYHGDCECSFS4BJ2SNTQP49V"
	checkout := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, RCPath: checkout + "/.envrc"}, env: map[string]string{"TOKEN": "value"}}
	hooks := runtimeHooks(t, d)
	hooks.Options = func(_ context.Context, id, name string) (map[string]any, error) {
		return map[string]any{"load": id != repository}, nil
	}
	a := &App{Preparation: hooks}
	env, _, _, err := a.agentRuntimeEnvironment(context.Background(), withRepository(claimedManifest(checkout), repository), agent.ModeTUI)
	if err != nil || env["TOKEN"] != "" || d.allowCalls != 0 || d.envCalls != 0 {
		t.Fatalf("env=%v allow=%d environment=%d err=%v", env, d.allowCalls, d.envCalls, err)
	}
}

func TestAgentRuntimeEnvironmentSetsTheRepositoryVariablesBeforeDirenv(t *testing.T) {
	repository := "01KZYHGDCECSFS4BJ2SNTQP49V"
	path := t.TempDir()
	d := &runtimeDirenv{status: direnvpkg.Status{Found: true, Allowed: true, RCPath: path + "/.envrc"}, env: map[string]string{"PORT": "from-envrc"}}
	a := &App{Preparation: runtimeHooks(t, d), RepositoryEnvironment: func(_ context.Context, id string) ([]model.EnvVar, error) {
		if id != repository {
			t.Fatalf("repository %q", id)
		}
		return []model.EnvVar{{Key: "DATABASE_URL", Value: "postgres://db"}, {Key: "PORT", Value: "8080"}}, nil
	}}
	env, _, _, err := a.agentRuntimeEnvironment(context.Background(), withRepository(claimedManifest(path), repository), agent.ModeHeadless)
	if err != nil || env["DATABASE_URL"] != "postgres://db" || env["PORT"] != "from-envrc" {
		t.Fatalf("env=%v err=%v", env, err)
	}
}

func TestAgentRuntimeEnvironmentKeepsWiOnPath(t *testing.T) {
	for _, tt := range []struct{ name, self, path, project, want string }{
		{"stale server", "/home/user/go/bin/wi", "/clean/bin", "", "/home/user/go/bin:/clean/bin"},
		{"already present", "/home/user/go/bin/wi", "/clean/bin:/home/user/go/bin", "", "/clean/bin:/home/user/go/bin"},
		{"direnv replacement", "/home/user/go/bin/wi", "/clean/bin", "/project/bin", "/home/user/go/bin:/project/bin"},
		{"empty path", "/home/user/go/bin/wi", "", "", "/home/user/go/bin"},
		{"unset executable", "", "/clean/bin", "", "/clean/bin"},
		{"relative executable", "wi", "/clean/bin", "", "/clean/bin"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", "/other-item/bin")
			path := t.TempDir()
			d := &runtimeDirenv{status: direnvpkg.Status{Found: tt.project != "", Allowed: true, RCPath: filepath.Join(path, ".envrc")}, env: map[string]string{"PATH": tt.project}}
			a := &App{SelfPath: tt.self, Preparation: runtimeHooks(t, d), Tmux: fakeBaseTmux{global: map[string]string{"PATH": tt.path, "HOME": "/home/user"}}}
			m := claimedManifest(path)
			env, _, _, err := a.agentRuntimeEnvironment(context.Background(), m, agent.ModeTUI)
			if err != nil || env["PATH"] != tt.want {
				t.Fatalf("path=%q want=%q error=%v", env["PATH"], tt.want, err)
			}
			if slices.Contains(filepath.SplitList(env["PATH"]), "/other-item/bin") {
				t.Fatalf("imported caller PATH %s", env["PATH"])
			}
			if tt.self != "" && filepath.IsAbs(tt.self) && strings.Count(env["PATH"], filepath.Dir(tt.self)) != 1 {
				t.Fatalf("duplicated executable directory %q", env["PATH"])
			}
		})
	}
}

func TestRuntimeEnvironmentHooksReceiveIdentityInBothModes(t *testing.T) {
	t.Setenv("EXTENSION_TEST_REMOVE", "stale")
	for _, mode := range []agent.Mode{agent.ModeTUI, agent.ModeHeadless} {
		calls := 0
		a := &App{Tmux: fakeBaseTmux{global: map[string]string{"PATH": "/bin", "HOME": "/home/operator", "EXTENSION_TEST_REMOVE": "stale"}}}
		a.Preparation = plugin.Pipeline{Hooks: []plugin.Hooks{{Name: "fixture", Sanitize: func(context.Context, plugin.EnvironmentRequest, map[string]any) (plugin.Patch, error) {
			return plugin.Patch{Warnings: []string{"sanitizer warning"}}, nil
		}, RuntimeEnvironment: func(ctx context.Context, r plugin.EnvironmentRequest, _ map[string]any) (plugin.Patch, error) {
			calls++
			if r.Subject.RepositoryID != "repo" || r.Subject.ItemID != "item" || r.Subject.Mode != string(mode) || r.Subject.ClaimID != "claim" || r.Subject.Generation != 7 {
				t.Fatal(r.Subject)
			}
			return plugin.Patch{Set: map[string]string{"HOOK_VALUE": "ready"}, Unset: []string{"EXTENSION_TEST_REMOVE"}}, nil
		}}}}
		m := model.Manifest{ID: "item", Repository: model.Repository{ID: "repo"}, Checkout: model.Checkout{ClaimGeneration: 7, Claim: &model.WorkspaceClaim{ID: "claim", Generation: 7, Path: t.TempDir()}}}
		env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), m, mode)
		if err != nil || calls != 1 || env["HOOK_VALUE"] != "ready" || env["EXTENSION_TEST_REMOVE"] != "" || len(warnings) != 1 {
			t.Fatalf("mode=%s calls=%d err=%v", mode, calls, err)
		}
	}
}

type forbiddenEnvironmentTmux struct {
	fakeBaseTmux
	t *testing.T
}

func (f forbiddenEnvironmentTmux) GlobalEnvironment(context.Context) (map[string]string, error) {
	f.t.Error("headless inspected tmux global environment")
	return nil, nil
}
func (f forbiddenEnvironmentTmux) SessionEnvironment(context.Context, string) (map[string]string, error) {
	f.t.Error("headless inspected tmux session environment")
	return nil, nil
}
func TestHeadlessEnvironmentPreservesCallerDataRootWithoutTmux(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("PI_SESSION_ID", "parent-conversation")
	t.Setenv("WI_ID", "parent-item")
	a := &App{Tmux: forbiddenEnvironmentTmux{t: t}}
	env, _, _, err := a.agentRuntimeEnvironment(context.Background(), model.Manifest{}, agent.ModeHeadless)
	if err != nil {
		t.Fatal(err)
	}
	if env["HOME"] != home || env["XDG_DATA_HOME"] != data || env["PI_SESSION_ID"] != "" || env["WI_ID"] != "" {
		t.Fatalf("unexpected headless root/identity HOME=%s XDG_DATA_HOME=%s PI_SESSION_ID=%s WI_ID=%s", env["HOME"], env["XDG_DATA_HOME"], env["PI_SESSION_ID"], env["WI_ID"])
	}
}
