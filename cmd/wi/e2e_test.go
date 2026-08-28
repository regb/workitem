package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCLIStateAndAttentionEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary end-to-end test in short mode")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for the CLI end-to-end test")
	}

	root := repositoryRoot(t)
	temp := t.TempDir()
	binary := filepath.Join(temp, "wi")
	build := exec.Command("go", "build", "-o", binary, "./cmd/wi")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real wi binary: %v\n%s", err, output)
	}
	releaseBinary := filepath.Join(temp, "wi-release")
	releaseBuild := exec.Command("go", "build", "-ldflags", "-X github.com/regb/workitem/internal/version.release=v0.0.0-test", "-o", releaseBinary, "./cmd/wi")
	releaseBuild.Dir = root
	if output, err := releaseBuild.CombinedOutput(); err != nil {
		t.Fatalf("build release wi binary: %v\n%s", err, output)
	}

	repository := filepath.Join(temp, "repository")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, repository, "init", "-q")
	runGitE2E(t, repository, "config", "user.name", "wi test")
	runGitE2E(t, repository, "config", "user.email", "wi@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("temporary repository\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, repository, "add", "README.md")
	runGitE2E(t, repository, "commit", "-q", "-m", "initial")

	// The tested binary sees git but deliberately has no tmux executable in
	// PATH. State and durable attention plumbing must not need it.
	tools := filepath.Join(temp, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(tools, "git")); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(temp, "home")
	dataHome := filepath.Join(temp, "data")
	configHome := filepath.Join(temp, "config")
	cacheHome := filepath.Join(temp, "cache")
	stateHome := filepath.Join(temp, "state")
	runtimeDir := filepath.Join(temp, "runtime")
	for _, dir := range []string{home, dataHome, configHome, cacheHome, stateHome, runtimeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := isolatedE2EEnv(map[string]string{
		"HOME": home, "XDG_DATA_HOME": dataHome, "XDG_CONFIG_HOME": configHome, "XDG_CACHE_HOME": cacheHome, "XDG_STATE_HOME": stateHome, "XDG_RUNTIME_DIR": runtimeDir,
		"PATH": tools, "NO_COLOR": "1", "GIT_CONFIG_NOSYSTEM": "1",
	})
	t.Cleanup(func() { stopE2EDaemon(t, binary, repository, env) })

	versionJSON := runWiE2E(t, binary, repository, env, "version", "--json")
	var version struct {
		Version   string `json:"version"`
		Revision  string `json:"revision"`
		GoVersion string `json:"go_version"`
	}
	decodeE2EJSON(t, versionJSON, &version)
	if version.Version == "" || version.Revision == "" || version.GoVersion == "" {
		t.Fatalf("real binary version = %+v", version)
	}
	var released struct {
		Version  string `json:"version"`
		Release  string `json:"release"`
		Revision string `json:"revision"`
	}
	decodeE2EJSON(t, runWiE2E(t, releaseBinary, repository, env, "version", "--json"), &released)
	if released.Version != "v0.0.0-test" || released.Release != "v0.0.0-test" || released.Revision == "" {
		t.Fatalf("release binary version = %+v", released)
	}

	createdJSON := runWiE2E(t, binary, repository, env, "new", "--json", "State and attention E2E")
	var created struct {
		WorkItemID string `json:"work_item_id"`
		State      string `json:"state"`
	}
	decodeE2EJSON(t, createdJSON, &created)
	if created.WorkItemID == "" || created.State != "backlog" {
		t.Fatalf("new result = %+v", created)
	}

	daemonStatusJSON := runWiE2E(t, binary, repository, env, "daemon", "status", "--json")
	var daemonStatus struct {
		SocketPath      string `json:"socket_path"`
		AgentSocketPath string `json:"agent_socket_path"`
	}
	decodeE2EJSON(t, daemonStatusJSON, &daemonStatus)
	if daemonStatus.AgentSocketPath == "" {
		t.Fatal("daemon did not publish its agent socket")
	}
	operatorAsAgentEnv := append(append([]string{}, env...), "WI_AGENT_DAEMON_SOCKET="+daemonStatus.SocketPath)
	assertWiE2EFails(t, binary, repository, operatorAsAgentEnv, "list")
	agentEnv := append(append([]string{}, env...), "WI_AGENT_DAEMON_SOCKET="+daemonStatus.AgentSocketPath)
	agentCreatedJSON := runWiE2E(t, binary, repository, agentEnv, "new", "--json", "Agent filed backlog item")
	var agentCreated struct {
		WorkItemID string `json:"work_item_id"`
		State      string `json:"state"`
	}
	decodeE2EJSON(t, agentCreatedJSON, &agentCreated)
	if agentCreated.WorkItemID == "" || agentCreated.State != "backlog" {
		t.Fatalf("agent new result = %+v", agentCreated)
	}
	assertWiE2EFails(t, binary, repository, agentEnv, "state", "set", "working", "--item", agentCreated.WorkItemID)
	assertWiE2EFails(t, binary, repository, agentEnv, "new", "--prompt", "start this", "Agent cannot start work")
	assertE2EState(t, binary, repository, env, agentCreated.WorkItemID, "backlog")

	assertE2EState(t, binary, repository, env, created.WorkItemID, "backlog")
	setState := func(target string) {
		t.Helper()
		output := runWiE2E(t, binary, repository, env, "state", "set", target, "--item", created.WorkItemID, "--json")
		var result struct {
			PreviousState string `json:"previous_state"`
			State         string `json:"state"`
			Changed       bool   `json:"changed"`
			Manifest      struct {
				Checkout struct {
					Path *string `json:"path"`
				} `json:"checkout"`
			} `json:"manifest"`
		}
		decodeE2EJSON(t, output, &result)
		if result.State != target || !result.Changed || result.Manifest.Checkout.Path != nil {
			t.Fatalf("state set %s = %+v", target, result)
		}
	}

	setState("working")
	activityJSON := runWiE2E(t, binary, repository, env, "attention", "activity", "--item", created.WorkItemID, "--json")
	var activity struct {
		WorkItemID string `json:"work_item_id"`
		Activity   struct {
			LastDeferredAt *time.Time `json:"last_deferred_at"`
		} `json:"activity"`
		Warnings []string `json:"warnings"`
	}
	decodeE2EJSON(t, activityJSON, &activity)
	if activity.WorkItemID != created.WorkItemID || activity.Activity.LastDeferredAt != nil {
		t.Fatalf("initial activity = %+v", activity)
	}
	for _, warning := range activity.Warnings {
		if strings.Contains(strings.ToLower(warning), "tmux") {
			t.Fatalf("durable activity attempted tmux inspection: %q", warning)
		}
	}

	deferJSON := runWiE2E(t, binary, repository, env, "attention", "defer", "--item", created.WorkItemID, "--json")
	var deferred struct {
		WorkItemID string    `json:"work_item_id"`
		DeferredAt time.Time `json:"deferred_at"`
		Warnings   []string  `json:"warnings"`
	}
	decodeE2EJSON(t, deferJSON, &deferred)
	if deferred.WorkItemID != created.WorkItemID || deferred.DeferredAt.IsZero() {
		t.Fatalf("defer result = %+v", deferred)
	}
	for _, warning := range deferred.Warnings {
		if strings.Contains(strings.ToLower(warning), "tmux") {
			t.Fatalf("durable defer attempted tmux inspection: %q", warning)
		}
	}

	activityJSON = runWiE2E(t, binary, repository, env, "attention", "activity", created.WorkItemID, "--json")
	decodeE2EJSON(t, activityJSON, &activity)
	if activity.Activity.LastDeferredAt == nil || !activity.Activity.LastDeferredAt.Equal(deferred.DeferredAt) {
		t.Fatalf("activity after defer = %+v; defer=%+v", activity, deferred)
	}

	eventsJSON := runWiE2E(t, binary, repository, env, "events", created.WorkItemID, "--json")
	var events struct {
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	decodeE2EJSON(t, eventsJSON, &events)
	if !hasE2EEvent(events.Events, "work_item.state_set") || !hasE2EEvent(events.Events, "attention.deferred") {
		t.Fatalf("events = %+v", events.Events)
	}

	setState("waiting")
	setState("working")
	setState("archived")
	setState("backlog")
	assertE2EState(t, binary, repository, env, created.WorkItemID, "backlog")

	entries, err := os.ReadDir(filepath.Join(dataHome, "wi", "worktrees"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("state/attention flow created worktrees: %+v", entries)
	}
	daemonLogs, err := filepath.Glob(filepath.Join(stateHome, "wi", "*", "daemon.log"))
	if err != nil || len(daemonLogs) != 1 {
		t.Fatalf("daemon state logs = %+v err=%v", daemonLogs, err)
	}
	if _, err := os.Stat(filepath.Join(cacheHome, "wi", "daemon.log")); !os.IsNotExist(err) {
		t.Fatalf("daemon log leaked into cache: %v", err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate e2e test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func runGitE2E(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func stopE2EDaemon(t *testing.T, binary, dir string, env []string) {
	t.Helper()
	stop := exec.Command(binary, "daemon", "stop", "--json")
	stop.Dir = dir
	stop.Env = env
	_ = stop.Run()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := exec.Command(binary, "daemon", "status", "--json")
		status.Dir = dir
		status.Env = env
		if err := status.Run(); err != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Error("end-to-end daemon remained online after cleanup")
}

func runWiE2E(t *testing.T, binary, dir string, env []string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("wi %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

func assertWiE2EFails(t *testing.T, binary, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("wi %s unexpectedly succeeded:\n%s", strings.Join(args, " "), output)
	}
}

func isolatedE2EEnv(overrides map[string]string) []string {
	replaced := map[string]bool{}
	for key := range overrides {
		replaced[key] = true
	}
	for _, key := range []string{"WI_ID", "WI_DIR", "WI_REPOSITORY", "WI_WORKTREE", "WI_DAEMON_SOCKET", "WI_AGENT_DAEMON_SOCKET", "TMUX", "TMUX_PANE", "PI_CODING_AGENT_SESSION_DIR"} {
		replaced[key] = true
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !replaced[key] {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func decodeE2EJSON(t *testing.T, data []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, data)
	}
}

func assertE2EState(t *testing.T, binary, dir string, env []string, itemID, expected string) {
	t.Helper()
	output := runWiE2E(t, binary, dir, env, "state", "show", "--item", itemID, "--json")
	var state struct {
		WorkItemID string `json:"work_item_id"`
		State      string `json:"state"`
	}
	decodeE2EJSON(t, output, &state)
	if state.WorkItemID != itemID || state.State != expected {
		t.Fatalf("state show = %+v; want %s", state, expected)
	}
}

func hasE2EEvent(events []struct {
	Type string `json:"type"`
}, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
