package pi_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/pi"
)

type recordingRunner struct {
	path     string
	args     []string
	cwd      string
	env      []string
	headless bool
	logPath  string
}

func (r *recordingRunner) RunInteractive(ctx context.Context, path string, args []string, cwd string, env []string) error {
	r.path = path
	r.args = append([]string(nil), args...)
	r.cwd = cwd
	r.env = append([]string(nil), env...)
	return nil
}

func TestArgsUsesExplicitSessionPath(t *testing.T) {
	args, err := pi.Args("/tmp/item/sessions/pi/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--session", "/tmp/item/sessions/pi/session.jsonl"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func (r *recordingRunner) RunHeadless(ctx context.Context, path string, args []string, env []string, spec pi.ExecSpec) error {
	r.path = path
	r.args = append([]string(nil), args...)
	r.cwd = spec.CWD
	r.env = append([]string(nil), env...)
	r.headless = true
	r.logPath = spec.LogPath
	return nil
}

func TestExecCommandConstructionByMode(t *testing.T) {
	for _, tt := range []struct {
		mode     agent.Mode
		wantArgs []string
		headless bool
	}{
		{mode: agent.ModeTUI, wantArgs: []string{"--extension", "/tmp/bridge.ts", "--session", "/tmp/item/sessions/pi/session.jsonl"}},
		{mode: agent.ModeRPC, wantArgs: []string{"--mode", "rpc", "--session", "/tmp/item/sessions/pi/session.jsonl"}, headless: true},
	} {
		t.Run(string(tt.mode), func(t *testing.T) {
			r := &recordingRunner{}
			client := pi.Client{Path: "pi", Runner: r}
			err := client.ExecMode(context.Background(), pi.ExecSpec{SessionPath: "/tmp/item/sessions/pi/session.jsonl", BridgePath: "/tmp/bridge.ts", Mode: tt.mode, CWD: "/tmp/worktree", Env: map[string]string{"WI_ID": "abc"}, LogPath: "/tmp/pi.log"})
			if err != nil {
				t.Fatal(err)
			}
			if r.path != "pi" || !reflect.DeepEqual(r.args, tt.wantArgs) || r.headless != tt.headless {
				t.Fatalf("runner = %+v, want args=%#v headless=%v", r, tt.wantArgs, tt.headless)
			}
			if r.cwd != "/tmp/worktree" || !envContains(r.env, "WI_ID=abc") {
				t.Fatalf("cwd/env = %q %v", r.cwd, r.env)
			}
		})
	}
}

func envContains(env []string, kv string) bool {
	for _, item := range env {
		if item == kv {
			return true
		}
	}
	return false
}
