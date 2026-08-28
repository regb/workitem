package tmux_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/tmux"
)

type call struct {
	Path string
	Args []string
	Env  []string
}

type recordingRunner struct {
	calls []call
	outs  [][]byte
	errs  []error
}

func (r *recordingRunner) Run(ctx context.Context, path string, args []string, env []string) ([]byte, error) {
	r.calls = append(r.calls, call{Path: path, Args: append([]string(nil), args...), Env: append([]string(nil), env...)})
	idx := len(r.calls) - 1
	var out []byte
	var err error
	if idx < len(r.outs) {
		out = r.outs[idx]
	}
	if idx < len(r.errs) {
		err = r.errs[idx]
	}
	return out, err
}

func TestListSessionsSortsAndIgnoresEmptyLines(t *testing.T) {
	r := &recordingRunner{outs: [][]byte{[]byte("wi-z\n\nwi-a\n")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sessions, []string{"wi-a", "wi-z"}) {
		t.Fatalf("sessions = %#v", sessions)
	}
	if !reflect.DeepEqual(r.calls[0].Args, []string{"list-sessions", "-F", "#{session_name}"}) {
		t.Fatalf("args = %#v", r.calls[0].Args)
	}
}

func TestListSessionsTreatsAbsentServerAsEmpty(t *testing.T) {
	r := &recordingRunner{errs: []error{errors.New("no server running on /tmp/tmux: exit status 1")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestListSessionsReportsInspectionFailure(t *testing.T) {
	r := &recordingRunner{errs: []error{errors.New("permission denied: exit status 1")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	if _, err := client.ListSessions(context.Background()); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureSessionCommandConstruction(t *testing.T) {
	// has-session fails (session missing), so the session is created and its
	// environment is scrubbed of any variables from another worktree that the
	// invoking client inherited.
	r := &recordingRunner{errs: []error{errors.New("exit status 1")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	created, err := client.EnsureSession(context.Background(), tmux.SessionSpec{
		Name: "wi-fix-01K1AB",
		CWD:  "/tmp/worktree",
		Env: map[string]string{
			"WI_ID":         "01K1ABCDE0000000000000000",
			"WI_DIR":        "/tmp/item",
			"WI_WORKTREE":   "/tmp/worktree",
			"WI_REPOSITORY": "/tmp/repo",
		},
		Scrub: []string{"STALE_PROJECT_VAR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected session to be created")
	}
	want := [][]string{
		{"has-session", "-t", "wi-fix-01K1AB"},
		{"new-session", "-d", "-s", "wi-fix-01K1AB", "-c", "/tmp/worktree", "-n", "agent"},
		{"set-environment", "-t", "wi-fix-01K1AB", "STALE_PROJECT_VAR", ""},
		{"set-environment", "-t", "wi-fix-01K1AB", "WI_DIR", "/tmp/item"},
		{"set-environment", "-t", "wi-fix-01K1AB", "WI_ID", "01K1ABCDE0000000000000000"},
		{"set-environment", "-t", "wi-fix-01K1AB", "WI_REPOSITORY", "/tmp/repo"},
		{"set-environment", "-t", "wi-fix-01K1AB", "WI_WORKTREE", "/tmp/worktree"},
		{"new-window", "-t", "wi-fix-01K1AB:", "-n", "shell", "-c", "/tmp/worktree"},
		{"select-window", "-t", "wi-fix-01K1AB:agent"},
	}
	if len(r.calls) != len(want) {
		t.Fatalf("call count = %d, want %d: %+v", len(r.calls), len(want), r.calls)
	}
	for i := range want {
		if !reflect.DeepEqual(r.calls[i].Args, want[i]) {
			t.Fatalf("call %d args = %#v, want %#v", i, r.calls[i].Args, want[i])
		}
	}
	if !envContains(r.calls[1].Env, "WI_ID=01K1ABCDE0000000000000000") {
		t.Fatalf("new-session env does not contain WI_ID: %v", r.calls[1].Env)
	}
	if envContains(r.calls[1].Env, "STALE_PROJECT_VAR=") {
		t.Fatalf("new-session env contains scrub variable: %v", r.calls[1].Env)
	}
}

func TestLaunchCommandUsesAgentWindow(t *testing.T) {
	r := &recordingRunner{outs: [][]byte{nil, []byte("agent\nshell\n")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	if err := client.LaunchCommand(context.Background(), tmux.LaunchSpec{
		SessionName:      "wi-session",
		CWD:              "/tmp/worktree",
		Env:              map[string]string{"WI_ID": "abc"},
		Command:          []string{"/path with space/wi", "agent", "exec", "--item", "abc", "--session", "def"},
		ReuseAgentWindow: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("call count = %d", len(r.calls))
	}
	if !reflect.DeepEqual(r.calls[0].Args, []string{"set-environment", "-t", "wi-session", "WI_ID", "abc"}) {
		t.Fatalf("environment set args = %#v", r.calls[0].Args)
	}
	if !reflect.DeepEqual(r.calls[1].Args, []string{"list-windows", "-t", "wi-session", "-F", "#{window_name}"}) {
		t.Fatalf("inspection args = %#v", r.calls[1].Args)
	}
	wantCommand := "'/path with space/wi' agent exec --item abc --session def"
	if !reflect.DeepEqual(r.calls[2].Args, []string{"respawn-pane", "-k", "-c", "/tmp/worktree", "-t", "wi-session:agent", wantCommand}) {
		t.Fatalf("launch args = %#v", r.calls[2].Args)
	}
}

func TestLaunchCommandRecreatesMissingAgentWindow(t *testing.T) {
	r := &recordingRunner{outs: [][]byte{nil, []byte("shell\n")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	if err := client.LaunchCommand(context.Background(), tmux.LaunchSpec{SessionName: "wi-session", CWD: "/tmp/worktree", Env: map[string]string{"WI_ID": "abc"}, Command: []string{"wi", "agent", "exec"}, ReuseAgentWindow: true}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("call count = %d", len(r.calls))
	}
	if !reflect.DeepEqual(r.calls[0].Args, []string{"set-environment", "-t", "wi-session", "WI_ID", "abc"}) {
		t.Fatalf("environment set args = %#v", r.calls[0].Args)
	}
	if !reflect.DeepEqual(r.calls[1].Args, []string{"list-windows", "-t", "wi-session", "-F", "#{window_name}"}) {
		t.Fatalf("window inspection args = %#v", r.calls[1].Args)
	}
	if !reflect.DeepEqual(r.calls[2].Args, []string{"new-window", "-t", "wi-session:", "-n", "agent", "-c", "/tmp/worktree", "wi agent exec"}) {
		t.Fatalf("new-window args = %#v", r.calls[2].Args)
	}
}

func TestLaunchCommandCreatesNewWindow(t *testing.T) {
	r := &recordingRunner{}
	client := tmux.Client{Path: "tmux", Runner: r}
	if err := client.LaunchCommand(context.Background(), tmux.LaunchSpec{
		SessionName: "wi-session",
		WindowName:  "alt",
		CWD:         "/tmp/worktree",
		Env:         map[string]string{"WI_ID": "abc"},
		Command:     []string{"wi", "agent", "exec", "--session", "def"},
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls[0].Args, []string{"set-environment", "-t", "wi-session", "WI_ID", "abc"}) {
		t.Fatalf("environment set args = %#v", r.calls[0].Args)
	}
	if !reflect.DeepEqual(r.calls[1].Args, []string{"new-window", "-t", "wi-session:", "-n", "alt", "-c", "/tmp/worktree", "wi agent exec --session def"}) {
		t.Fatalf("new-window args = %#v", r.calls[1].Args)
	}
}

func TestListPanesUsesOneBatchedCall(t *testing.T) {
	r := &recordingRunner{outs: [][]byte{[]byte("wi-a\tagent\t%1\t0\t123\tpi\t/tmp/a\nwi-b\tagent\t%2\t0\t456\tnode\t/tmp/b\n")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	panes, err := client.ListPanes(context.Background())
	if err != nil || len(panes) != 2 || panes[0].SessionName != "wi-a" || panes[1].PanePID != 456 {
		t.Fatalf("panes=%+v err=%v", panes, err)
	}
	if len(r.calls) != 1 || !reflect.DeepEqual(r.calls[0].Args[:3], []string{"list-panes", "-a", "-F"}) {
		t.Fatalf("calls=%+v", r.calls)
	}
}

func TestShellCommandQuotesSafely(t *testing.T) {
	got, err := tmux.ShellCommand([]string{"wi", "arg with spaces", "quote'arg"})
	if err != nil {
		t.Fatal(err)
	}
	want := "wi 'arg with spaces' 'quote'\\''arg'"
	if got != want {
		t.Fatalf("ShellCommand = %q, want %q", got, want)
	}
}

func TestKillSessionAsyncCommandConstruction(t *testing.T) {
	r := &recordingRunner{}
	client := tmux.Client{Path: "tmux", Runner: r}
	if err := client.KillSessionAsync(context.Background(), "wi-session"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls[0].Args, []string{"run-shell", "-b", "sleep 0.2; tmux kill-session -t wi-session"}) {
		t.Fatalf("async kill args = %#v", r.calls[0].Args)
	}
}

func TestAttachOrSwitchCommandConstruction(t *testing.T) {
	r := &recordingRunner{outs: [][]byte{nil, []byte("/dev/pts/42\n")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	if err := client.AttachOrSwitch(context.Background(), "wi-session", false); err != nil {
		t.Fatal(err)
	}
	if err := client.AttachOrSwitch(context.Background(), "wi-session", true); err != nil {
		t.Fatal(err)
	}
	if err := client.AttachOrSwitchClient(context.Background(), "wi-new-session", true, "client-7"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls[0].Args, []string{"attach-session", "-t", "wi-session"}) {
		t.Fatalf("outside tmux args = %#v", r.calls[0].Args)
	}
	if !reflect.DeepEqual(r.calls[1].Args, []string{"display-message", "-p", "#{client_name}"}) {
		t.Fatalf("client lookup args = %#v", r.calls[1].Args)
	}
	if !reflect.DeepEqual(r.calls[2].Args, []string{"switch-client", "-c", "/dev/pts/42", "-t", "wi-session"}) {
		t.Fatalf("inside tmux args = %#v", r.calls[2].Args)
	}
	if !reflect.DeepEqual(r.calls[3].Args, []string{"switch-client", "-c", "client-7", "-t", "wi-new-session"}) {
		t.Fatalf("explicit client args = %#v", r.calls[3].Args)
	}
}

func TestAttachOrSwitchRecoversUnexpandedClientFormat(t *testing.T) {
	r := &recordingRunner{outs: [][]byte{[]byte("/dev/pts/42\n")}}
	client := tmux.Client{Path: "tmux", Runner: r}
	if err := client.AttachOrSwitchClient(context.Background(), "wi-session", true, "#{client_name}"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls[0].Args, []string{"display-message", "-p", "#{client_name}"}) || !reflect.DeepEqual(r.calls[1].Args, []string{"switch-client", "-c", "/dev/pts/42", "-t", "wi-session"}) {
		t.Fatalf("calls=%+v", r.calls)
	}
}

func envContains(env []string, want string) bool {
	for _, value := range env {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}
