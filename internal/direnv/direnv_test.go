package direnv_test

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/testutil"
)

type recordingRunner struct {
	args   []string
	dir    string
	env    []string
	output []byte
}

func (r *recordingRunner) Run(_ context.Context, _ string, args []string, dir string, env []string) ([]byte, error) {
	r.args = append([]string{}, args...)
	r.dir = dir
	r.env = append([]string{}, env...)
	return r.output, nil
}

func TestDenyUsesAbsoluteEnvrcPath(t *testing.T) {
	runner := &recordingRunner{}
	client := direnv.Client{Path: "direnv", Runner: runner}
	if err := client.Deny(context.Background(), "/repo/.envrc"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.args, []string{"deny", "/repo/.envrc"}) || runner.dir != "/repo" {
		t.Fatalf("args=%v dir=%q", runner.args, runner.dir)
	}
}

func TestEnvironmentSeparatesJSONFromStderrDiagnostics(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-direnv")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'direnv: loading .envrc' >&2\nprintf 'SAFE=value\\0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	client := direnv.Client{Path: script, Runner: direnv.ExecRunner{}}
	got, err := client.Environment(context.Background(), t.TempDir(), nil)
	if err != nil || got["SAFE"] != "value" {
		t.Fatalf("environment=%v err=%v", got, err)
	}
}

func TestEnvironment(t *testing.T) {
	runner := &recordingRunner{output: []byte("API_TOKEN=secret\x00PATH=/custom/bin\x00")}
	client := direnv.Client{Path: "direnv", Runner: runner}
	got, err := client.Environment(context.Background(), "/repo", map[string]string{"HOME": "/home/user", "PATH": "/clean/bin"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"API_TOKEN": "secret", "PATH": "/custom/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%v want=%v", got, want)
	}
	if !reflect.DeepEqual(runner.args, []string{"exec", "/repo", "env", "-0"}) || runner.dir != "/repo" {
		t.Fatalf("args=%v dir=%q", runner.args, runner.dir)
	}
	if len(runner.env) != 2 || !strings.Contains(strings.Join(runner.env, "\x00"), "HOME=/home/user") || !strings.Contains(strings.Join(runner.env, "\x00"), "PATH=/clean/bin") {
		t.Fatalf("base env=%v", runner.env)
	}
}

func TestEnvironmentReloadsWatchedSourceWhenCallerHasPreviouslyLoadedEnvironment(t *testing.T) {
	path, err := exec.LookPath("direnv")
	if err != nil {
		t.Skip("direnv is not installed")
	}
	dir := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(dir); resolveErr == nil {
		dir = resolved
	}
	values := filepath.Join(dir, "values.envrc")
	if err := os.WriteFile(values, []byte("export WI_DIRENV_RELOAD_SENTINEL=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("source_env_if_exists ./values.envrc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	dataHome := filepath.Join(home, "data")
	configHome := filepath.Join(home, "config")
	cacheHome := filepath.Join(home, "cache")
	baseEnv := map[string]string{"HOME": home, "PATH": os.Getenv("PATH"), "XDG_DATA_HOME": dataHome, "XDG_CONFIG_HOME": configHome, "XDG_CACHE_HOME": cacheHome}
	allow := exec.Command(path, "allow", filepath.Join(dir, ".envrc"))
	allow.Dir = dir
	allow.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "XDG_DATA_HOME=" + dataHome, "XDG_CONFIG_HOME=" + configHome, "XDG_CACHE_HOME=" + cacheHome}
	if output, err := allow.CombinedOutput(); err != nil {
		t.Fatalf("allow fixture: %v: %s", err, output)
	}
	client := direnv.Client{Path: path, Runner: direnv.ExecRunner{}}
	loaded, err := client.Environment(context.Background(), dir, baseEnv)
	if err != nil || loaded["WI_DIRENV_RELOAD_SENTINEL"] != "old" {
		t.Fatalf("initial environment sentinel=%q err=%v", loaded["WI_DIRENV_RELOAD_SENTINEL"], err)
	}
	if err := os.WriteFile(values, []byte("export WI_DIRENV_RELOAD_SENTINEL=fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Direnv's watch representation can use second-resolution mtimes. Make
	// the sourced file's next version observably newer without waiting.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(values, future, future); err != nil {
		t.Fatal(err)
	}
	// This child deliberately tests an already-loaded direnv environment;
	// HelperProcess keeps the parent test namespace marker so TestMain does
	// not scrub that fixture.
	extra := maps.Clone(loaded)
	extra["WI_DIRENV_HELPER_DIR"], extra["WI_DIRENV_HELPER_PATH"] = dir, path
	args, env := testutil.HelperProcess("TestEnvironmentReloadHelper", extra)
	command := exec.Command(args[0], args[1:]...)
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	// Judge only the helper's stdout: a coverage-instrumented helper adds a
	// GOCOVERDIR warning to stderr when it exits.
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "fresh" {
		t.Fatalf("reloaded environment output=%q stderr=%q err=%v", output, stderr.String(), err)
	}
}

func TestEnvironmentReloadHelper(t *testing.T) {
	if !testutil.InHelperProcess("TestEnvironmentReloadHelper") {
		return
	}
	client := direnv.Client{Path: os.Getenv("WI_DIRENV_HELPER_PATH"), Runner: direnv.ExecRunner{}}
	environment, err := client.Environment(context.Background(), os.Getenv("WI_DIRENV_HELPER_DIR"), envFromProcess(os.Environ()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, environment["WI_DIRENV_RELOAD_SENTINEL"])
	os.Exit(0)
}

func envFromProcess(entries []string) map[string]string {
	env := map[string]string{}
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	return env
}

func TestManagedVariables(t *testing.T) {
	for _, tc := range []struct {
		diff string
		want map[string]bool
	}{
		{testutil.DirenvDiff(t, `{"p":{"PATH":"/x"},"n":{"PROJECT_GH_TOKEN":"","ISSUE_TRACKER_TOKEN":""}}`), map[string]bool{"PATH": true, "PROJECT_GH_TOKEN": true, "ISSUE_TRACKER_TOKEN": true}},
		{"not-base64!", map[string]bool{}},
		{"", map[string]bool{}},
	} {
		got := direnv.ManagedVariables(tc.diff)
		if len(got) != len(tc.want) {
			t.Errorf("ManagedVariables(%q) = %v, want %v", tc.diff, got, tc.want)
		}
		for name := range tc.want {
			if !got[name] {
				t.Errorf("ManagedVariables(%q) = %v, want %v", tc.diff, got, tc.want)
			}
		}
	}
}

func TestParseStatus(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   direnv.Status
	}{
		{"direnv exec path /usr/bin/direnv\nNo .envrc loaded\nFound RC path /repo/.envrc\nFound watch: \".envrc\" - 2026-07-31T00:00:00+02:00\nFound RC allowed true\nFound RC allowPath /home/me/.local/share/direnv/allow/abc\n", direnv.Status{Found: true, RCPath: "/repo/.envrc", Allowed: true}},
		{"Found RC path /repo/.envrc\nFound RC allowed 0\n", direnv.Status{Found: true, RCPath: "/repo/.envrc", Allowed: true}},
		{"Found RC path /repo/.envrc\nFound RC allowed 1\n", direnv.Status{Found: true, RCPath: "/repo/.envrc"}},
		{"No .envrc or .env found\n", direnv.Status{}},
	} {
		if got := direnv.ParseStatus(tc.output); got.Found != tc.want.Found || got.RCPath != tc.want.RCPath || got.Allowed != tc.want.Allowed {
			t.Errorf("ParseStatus(%q) = %+v, want %+v", tc.output, got, tc.want)
		}
	}
}
