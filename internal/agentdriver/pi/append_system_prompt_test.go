package pi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/agentdriver/pi"
)

type systemPromptRunner struct {
	recordingRunner
	t            *testing.T
	prompt, path string
	fail         bool
}

func (r *systemPromptRunner) inspect(args []string) error {
	r.t.Helper()
	count := 0
	for i, arg := range args {
		if arg == "--system-prompt" {
			r.t.Fatal("replaced native system prompt")
		}
		if arg != "--append-system-prompt" {
			continue
		}
		count++
		if i+1 >= len(args) {
			r.t.Fatal("missing prompt path")
		}
		r.path = args[i+1]
		data, err := os.ReadFile(r.path)
		if err != nil || string(data) != r.prompt {
			r.t.Fatalf("prompt=%q err=%v", data, err)
		}
		info, err := os.Stat(r.path)
		if err != nil || info.Mode().Perm() != 0600 {
			r.t.Fatal("prompt is not private", err)
		}
	}
	want := 1
	if strings.TrimSpace(r.prompt) == "" {
		want = 0
	}
	if count != want {
		r.t.Fatalf("flags=%d want=%d", count, want)
	}
	if r.fail {
		return errors.New("backend failed")
	}
	return nil
}
func (r *systemPromptRunner) RunInteractive(ctx context.Context, path string, args []string, cwd string, env []string) error {
	if err := r.recordingRunner.RunInteractive(ctx, path, args, cwd, env); err != nil {
		return err
	}
	return r.inspect(args)
}
func (r *systemPromptRunner) RunHeadless(ctx context.Context, path string, args []string, env []string, spec pi.ExecSpec) error {
	if err := r.recordingRunner.RunHeadless(ctx, path, args, env, spec); err != nil {
		return err
	}
	return r.inspect(args)
}
func TestAppendSystemPromptBothModesAndSessionStates(t *testing.T) {
	for _, mode := range []agent.Mode{agent.ModeTUI, agent.ModeHeadless} {
		for _, resume := range []bool{false, true} {
			for _, prompt := range []string{"Custom system instructions.\nKeep exact whitespace.", "", "   ", "/etc/passwd"} {
				for _, fail := range []bool{false, true} {
					t.Run(string(mode)+"/"+prompt, func(t *testing.T) {
						session := filepath.Join(t.TempDir(), "session.jsonl")
						if resume {
							if err := os.WriteFile(session, []byte("existing native history"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						runner := &systemPromptRunner{t: t, prompt: prompt, fail: fail}
						client := pi.Client{Path: "pi", Runner: runner}
						err := client.ExecMode(context.Background(), pi.ExecSpec{SessionPath: session, BridgePath: "/tmp/bridge.ts", Mode: mode, AppendSystemPrompt: prompt})
						if (err != nil) != fail {
							t.Fatalf("error=%v fail=%v", err, fail)
						}
						if runner.path != "" {
							if _, err := os.Stat(runner.path); !os.IsNotExist(err) {
								t.Fatal("temporary prompt survived", err)
							}
						}
						if resume {
							data, err := os.ReadFile(session)
							if err != nil || string(data) != "existing native history" {
								t.Fatal("native history changed", err)
							}
						}
					})
				}
			}
		}
	}
}
