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
		{"set-environment", "-r", "-t", "wi-fix-01K1AB", "STALE_PROJECT_VAR"},
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

func TestLaunchCommandSetsEnvironmentThenTargetsTheWindow(t *testing.T) {
	setEnv := []string{"set-environment", "-t", "wi-session", "WI_ID", "abc"}
	listWindows := []string{"list-windows", "-t", "wi-session", "-F", "#{window_name}"}
	for _, tc := range []struct {
		name    string
		windows string // list-windows output, when the agent window is reused
		spec    tmux.LaunchSpec
		want    [][]string
	}{
		{
			name:    "respawns the existing agent window",
			windows: "agent\nshell\n",
			spec:    tmux.LaunchSpec{Command: []string{"/path with space/wi", "agent", "exec", "--item", "abc", "--conversation", "def"}, ReuseAgentWindow: true},
			want:    [][]string{setEnv, listWindows, {"respawn-pane", "-k", "-c", "/tmp/worktree", "-t", "wi-session:agent", "'/path with space/wi' agent exec --item abc --conversation def"}},
		},
		{
			name:    "recreates a missing agent window",
			windows: "shell\n",
			spec:    tmux.LaunchSpec{Command: []string{"wi", "agent", "exec"}, ReuseAgentWindow: true},
			want:    [][]string{setEnv, listWindows, {"new-window", "-t", "wi-session:", "-n", "agent", "-c", "/tmp/worktree", "wi agent exec"}},
		},
		{
			name: "creates a named window",
			spec: tmux.LaunchSpec{WindowName: "alt", Command: []string{"wi", "agent", "exec", "--conversation", "def"}},
			want: [][]string{setEnv, {"new-window", "-t", "wi-session:", "-n", "alt", "-c", "/tmp/worktree", "wi agent exec --conversation def"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingRunner{outs: [][]byte{nil, []byte(tc.windows)}}
			spec := tc.spec
			spec.SessionName, spec.CWD, spec.Env = "wi-session", "/tmp/worktree", map[string]string{"WI_ID": "abc"}
			if err := (tmux.Client{Path: "tmux", Runner: r}).LaunchCommand(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			var got [][]string
			for _, c := range r.calls {
				got = append(got, c.Args)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("calls = %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// PaneInfo and SessionEnvironment parse tmux's output: tab-separated pane
// fields, and environment lines where "-NAME" marks a removed variable.
func TestPaneInfoAndSessionEnvironmentParseOutput(t *testing.T) {
	r := &recordingRunner{outs: [][]byte{
		[]byte("wi-a\tagent\t%1\t0\t123\tpi\t/tmp/a b\n"),
		[]byte("too\tfew\n"),
		[]byte("WI_ID=abc\n-REMOVED\nEMPTY=\nURL=a=b\nnoequals\n\n"),
	}}
	client := tmux.Client{Path: "tmux", Runner: r}
	pane, err := client.PaneInfo(context.Background(), "wi-a:agent")
	if want := (tmux.PaneInfo{SessionName: "wi-a", WindowName: "agent", PaneID: "%1", PaneIndex: "0", PanePID: 123, Command: "pi", CurrentPath: "/tmp/a b"}); err != nil || pane != want {
		t.Fatalf("pane = %+v err=%v", pane, err)
	}
	if _, err := client.PaneInfo(context.Background(), "wi-a:agent"); err == nil {
		t.Fatal("accepted short pane info")
	}
	if _, err := client.PaneInfo(context.Background(), " "); err == nil {
		t.Fatal("accepted empty target")
	}
	env, err := client.SessionEnvironment(context.Background(), "wi-a")
	if want := map[string]string{"WI_ID": "abc", "EMPTY": "", "URL": "a=b"}; err != nil || !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %#v err=%v", env, err)
	}
	if got := r.calls[len(r.calls)-1].Args; !reflect.DeepEqual(got, []string{"show-environment", "-t", "wi-a"}) {
		t.Fatalf("show-environment args = %#v", got)
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

func envContains(env []string, want string) bool {
	for _, value := range env {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}
