package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/regb/workitem/internal/testutil"
)

func TestOwnedInputOffPaneReplacement(t *testing.T) {
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	socket := filepath.Join(testutil.ShortTempDir(t), "tmux.sock")
	run := func(ctx context.Context, input io.Reader, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, executable, append([]string{"-S", socket}, args...)...)
		cmd.Env = os.Environ()
		cmd.Stdin = input
		data, err := cmd.CombinedOutput()
		return string(data), err
	}
	ctx := context.Background()
	if out, err := run(ctx, nil, "-f", "/dev/null", "new-session", "-d", "-s", "owned", "exec sleep 60"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	defer run(ctx, nil, "kill-server")
	out, err := run(ctx, nil, "display-message", "-p", "-t", "owned", "#{pane_id}|#{pane_pid}")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSpace(out), "|")
	owner, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	input := &tmuxInput{pane: fields[0], owner: owner, run: run}
	if _, err := run(ctx, nil, "select-pane", "-d", "-t", input.pane); err != nil {
		t.Fatal(err)
	}
	if err := input.verify(ctx, false); err != nil || !input.inputOff {
		t.Fatalf("owned read-only pane: %v", err)
	}
	// Respawn preserves pane_id but replaces its owning process group.
	if _, err := run(ctx, nil, "respawn-pane", "-k", "-t", input.pane, "exec sleep 60"); err != nil {
		t.Fatal(err)
	}
	if err := input.verify(ctx, false); err == nil || failureCode(err) != "pane_changed" {
		t.Fatalf("replacement accepted: %v", err)
	}
}

func TestInputPipeDoesNotReplaceExistingPipe(t *testing.T) {
	calls := 0
	input := &tmuxInput{pane: "%1", owner: syscall.Getpgrp(), run: func(_ context.Context, _ io.Reader, args ...string) (string, error) {
		calls++
		if args[0] == "display-message" {
			if args[len(args)-1] == "#{pane_pipe}" {
				return "1", nil
			}
			return "%1|" + fmt.Sprint(syscall.Getpgrp()) + "|0|0|1", nil
		}
		return "", fmt.Errorf("unexpected write")
	}}
	if err := input.pipeInput(context.Background(), "private"); err == nil {
		t.Fatal("existing pipe replaced")
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestInputKeyBytes(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		want string
	}{
		{[]string{"Enter"}, "\r"}, {[]string{"C-x", "C-s"}, "\x18\x13"}, {[]string{"Escape"}, "\x1b"},
		{[]string{"M-C-g", "BTab", "Space", "A"}, "\x1b\x07\x1b[Z A"},
	} {
		got, err := inputKeyBytes(tc.keys)
		if err != nil || got != tc.want {
			t.Fatalf("%v: %q %v", tc.keys, got, err)
		}
	}
	if _, err := inputKeyBytes([]string{"unknown"}); err == nil {
		t.Fatal("unsupported key encoded")
	}
}
