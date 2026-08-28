package direnv_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/direnv"
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
	values := filepath.Join(dir, "values.envrc")
	if err := os.WriteFile(values, []byte("export WI_DIRENV_RELOAD_SENTINEL=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("source_env_if_exists ./values.envrc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(path, "allow", filepath.Join(dir, ".envrc")).CombinedOutput(); err != nil {
		t.Fatalf("allow fixture: %v: %s", err, output)
	}
	client := direnv.Client{Path: path, Runner: direnv.ExecRunner{}}
	loaded, err := client.Environment(context.Background(), dir, nil)
	if err != nil || loaded["WI_DIRENV_RELOAD_SENTINEL"] != "old" {
		t.Fatalf("initial environment sentinel=%q err=%v", loaded["WI_DIRENV_RELOAD_SENTINEL"], err)
	}
	// Direnv's watch representation can use second-resolution mtimes. Ensure
	// the sourced file's next version is observably newer.
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(values, []byte("export WI_DIRENV_RELOAD_SENTINEL=fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=TestEnvironmentReloadHelper")
	command.Env = make([]string, 0, len(loaded)+3)
	for key, value := range loaded {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Env = append(command.Env, "WI_DIRENV_HELPER=1", "WI_DIRENV_HELPER_DIR="+dir, "WI_DIRENV_HELPER_PATH="+path)
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "fresh" {
		t.Fatalf("reloaded environment output=%q err=%v", output, err)
	}
}

func TestEnvironmentReloadHelper(t *testing.T) {
	if os.Getenv("WI_DIRENV_HELPER") != "1" {
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

func TestManagedVariablesDecodesDirenvDiff(t *testing.T) {
	compressed := encodeDirenvDiff(t, `{"p":{"PATH":"/x"},"n":{"PROJECT_GH_TOKEN":"","ISSUE_TRACKER_TOKEN":""}}`)
	vars := direnv.ManagedVariables(compressed)
	if !vars["PATH"] || !vars["PROJECT_GH_TOKEN"] || !vars["ISSUE_TRACKER_TOKEN"] {
		t.Fatalf("managed variables = %v", vars)
	}
}

func TestManagedVariablesIgnoresMalformedInput(t *testing.T) {
	if vars := direnv.ManagedVariables("not-base64!"); len(vars) != 0 {
		t.Fatalf("malformed input produced variables: %v", vars)
	}
}

func encodeDirenvDiff(t *testing.T, payload string) string {
	t.Helper()
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.URLEncoding.EncodeToString(buf.Bytes())
}

func TestParseStatusAcceptsNumericAllowedStatus(t *testing.T) {
	allowed := direnv.ParseStatus("Found RC path /repo/.envrc\nFound RC allowed 0\n")
	denied := direnv.ParseStatus("Found RC path /repo/.envrc\nFound RC allowed 1\n")
	if !allowed.Allowed || denied.Allowed {
		t.Fatalf("allowed=%+v denied=%+v", allowed, denied)
	}
}

func TestParseStatus(t *testing.T) {
	st := direnv.ParseStatus(`direnv exec path /usr/bin/direnv
No .envrc loaded
Found RC path /repo/.envrc
Found watch: ".envrc" - 2026-07-31T00:00:00+02:00
Found RC allowed true
Found RC allowPath /home/me/.local/share/direnv/allow/abc
`)
	if !st.Found || st.RCPath != "/repo/.envrc" || !st.Allowed {
		t.Fatalf("status = %+v", st)
	}
}
