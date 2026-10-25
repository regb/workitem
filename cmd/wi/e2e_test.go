package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
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
	// Go may omit VCS metadata for source archives or linked worktrees. The
	// CLI must report the revision actually embedded, including its absence.
	embedded, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	revision := ""
	for _, setting := range embedded.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
	}
	if version.Version == "" || version.Revision != revision || version.GoVersion == "" {
		t.Fatalf("real binary version = %+v", version)
	}
	var released struct {
		Version  string `json:"version"`
		Release  string `json:"release"`
		Revision string `json:"revision"`
	}
	decodeE2EJSON(t, runWiE2E(t, releaseBinary, repository, env, "version", "--json"), &released)
	if released.Version != "v0.0.0-test" || released.Release != "v0.0.0-test" || released.Revision != revision {
		t.Fatalf("release binary version = %+v", released)
	}

	configDir := filepath.Join(configHome, "wi")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	defaultProfile := "default_agent = \"pi\"\n[agents.pi]\ndriver = \"pi\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(defaultProfile), 0600); err != nil {
		t.Fatal(err)
	}
	runWiE2E(t, binary, repository, env, "repo", "add", "test", "--from", repository)
	if output := runWiE2E(t, binary, repository, env, "repo", "settings", "show", "test", "--json"); !strings.Contains(string(output), "\"trust\": \"prompt\"") {
		t.Fatalf("repository configuration lookup=%s", output)
	}
	exerciseRepositorySettingsE2E(t, binary, repository, env)
	exercisePluginCatalogE2E(t, binary, repository, env, configDir, defaultProfile)
	// Validate through the real bundled driver process, with neither Pi nor
	// tmux on PATH. Invalid settings must fail before any item is created.
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(defaultProfile+"[agents.invalid]\ndriver = \"pi\"\n[agents.invalid.config]\nreasoning = \"extreme\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	emptyList := string(runWiE2E(t, binary, repository, env, "list", "--ids"))
	for _, mode := range []string{"tui", "headless"} {
		invalid := exec.Command(binary, "new", "--repo", "test", "--title", "Invalid startup", "--agent", "invalid", "--start", "--agent-mode", mode)
		invalid.Dir, invalid.Env = repository, env
		output, err := invalid.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "invalid Pi reasoning level") || strings.Contains(string(output), "created work item") {
			t.Fatalf("preflight mode=%s err=%v output=%s", mode, err, output)
		}
		if list := string(runWiE2E(t, binary, repository, env, "list", "--ids")); list != emptyList {
			t.Fatalf("preflight changed the work list: %s", list)
		}
	}
	createdJSON := runWiE2E(t, binary, repository, env, "new", "--repo", "test", "--json", "--title", "State and attention E2E")
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
	agentCreatedJSON := runWiE2E(t, binary, repository, agentEnv, "new", "--repo", "test", "--json", "--untitled")
	var agentCreated struct {
		WorkItemID string         `json:"work_item_id"`
		State      string         `json:"state"`
		Manifest   model.Manifest `json:"manifest"`
	}
	decodeE2EJSON(t, agentCreatedJSON, &agentCreated)
	if agentCreated.WorkItemID == "" || agentCreated.State != "backlog" || agentCreated.Manifest.Title != model.UntitledTitle || agentCreated.Manifest.Slug != model.IDDerivedSlug(agentCreated.WorkItemID) || agentCreated.Manifest.Checkout.Specification.Branch != model.ItemBranchName(agentCreated.Manifest.Slug, agentCreated.WorkItemID) || agentCreated.Manifest.Checkout.Claim != nil || agentCreated.Manifest.PrimaryConversation != nil {
		t.Fatalf("agent new result = %+v", agentCreated)
	}
	assertWiE2EFails(t, binary, repository, agentEnv, "edit", "--item", agentCreated.WorkItemID, "--title", "Forbidden edit")
	titledJSON := runWiE2E(t, binary, repository, env, "edit", "--item", agentCreated.WorkItemID, "--title", "Clarified backlog objective", "--json")
	var titled struct {
		Title    string         `json:"title"`
		Changed  bool           `json:"changed"`
		Manifest model.Manifest `json:"manifest"`
	}
	decodeE2EJSON(t, titledJSON, &titled)
	if !titled.Changed || titled.Title != "Clarified backlog objective" || titled.Manifest.Slug != agentCreated.Manifest.Slug || titled.Manifest.Checkout.Specification.Branch != agentCreated.Manifest.Checkout.Specification.Branch || titled.Manifest.PrimaryConversation != nil || titled.Manifest.Checkout.Claim != nil {
		t.Fatalf("title edit changed identity or allocated resources: %+v", titled)
	}
	assertWiE2EFails(t, binary, repository, agentEnv, "state", "set", "active", "--item", agentCreated.WorkItemID)
	assertWiE2EFails(t, binary, repository, agentEnv, "new", "--start", "--untitled")
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
					Claim *struct {
						Path string `json:"path"`
					} `json:"claim"`
				} `json:"checkout"`
			} `json:"manifest"`
		}
		decodeE2EJSON(t, output, &result)
		if result.State != target || !result.Changed || result.Manifest.Checkout.Claim != nil {
			t.Fatalf("state set %s = %+v", target, result)
		}
	}

	setState("active")
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

	setState("backlog")
	setState("active")
	setState("closed")
	setState("backlog")
	assertE2EState(t, binary, repository, env, created.WorkItemID, "backlog")

	// Opting out of defaults must not parse broken declarative project labels,
	// including during the daemon\'s independent created-from pin verification.
	if err := os.MkdirAll(filepath.Join(repository, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".config", "wi.toml"), []byte("[item.defaults]\nlabels = [invalid]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, repository, "add", ".config/wi.toml")
	runGitE2E(t, repository, "commit", "-q", "-m", "broken project defaults")
	runWiE2E(t, binary, repository, env, "repo", "add", "invalid-project", "--from", repository)
	assertWiE2EFails(t, binary, repository, env, "new", "--repo", "invalid-project", "--title", "Cannot inherit invalid defaults")
	withoutDefaults := runWiE2E(t, binary, repository, env, "new", "--repo", "invalid-project", "--title", "Skip invalid defaults", "--no-default-labels", "--label", "explicit", "--json")
	var optedOut struct {
		Manifest struct {
			Labels []string `json:"labels"`
		} `json:"manifest"`
	}
	decodeE2EJSON(t, withoutDefaults, &optedOut)
	if len(optedOut.Manifest.Labels) != 1 || optedOut.Manifest.Labels[0] != "explicit" {
		t.Fatalf("opted-out labels = %v", optedOut.Manifest.Labels)
	}

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
	exerciseRepositoryArchiveE2E(t, binary, repository, env, created.WorkItemID)
}

func exerciseRepositorySettingsE2E(t *testing.T, binary, repository string, env []string) {
	t.Helper()
	var changed struct {
		Changed  bool   `json:"changed"`
		Revision uint64 `json:"revision"`
	}
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "settings", "set", "test", "plugins.direnv.trust", "always", "--json"), &changed)
	if !changed.Changed || changed.Revision != 1 {
		t.Fatal(changed)
	}
	var shown struct {
		Revision  uint64                                `json:"revision"`
		Overrides map[string]map[string]json.RawMessage `json:"overrides"`
		Effective map[string]map[string]any             `json:"effective"`
		Origins   map[string]string                     `json:"origins"`
	}
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "settings", "show", "test", "--json"), &shown)
	if shown.Revision != 1 || shown.Effective["plugins.direnv"]["trust"] != "always" || string(shown.Overrides["plugins.direnv"]["trust"]) != `"always"` {
		t.Fatalf("settings=%+v", shown)
	}
	assertWiE2EFails(t, binary, repository, env, "repo", "settings", "set", "test", "plugins.direnv.trust", "typo", "--json")
	assertWiE2EFails(t, binary, repository, env, "repo", "settings", "set", "test", "plugins.missing.trust", "always", "--json")
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "settings", "unset", "test", "plugins.direnv.trust", "--json"), &changed)
	if !changed.Changed || changed.Revision != 2 {
		t.Fatal(changed)
	}
	shown.Overrides = nil
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "settings", "show", "test", "--json"), &shown)
	if shown.Revision != 2 || shown.Effective["plugins.direnv"]["trust"] != "prompt" || len(shown.Overrides) != 0 {
		t.Fatalf("unset did not restore defaults: %+v", shown)
	}
}

func exerciseRepositoryArchiveE2E(t *testing.T, binary, repository string, env []string, itemID string) {
	t.Helper()
	runWiE2E(t, binary, repository, env, "workspace", "ensure", "--item", itemID, "--json")
	var shown struct {
		Manifest model.Manifest `json:"manifest"`
	}
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "show", itemID, "--json"), &shown)
	worktree := shown.Manifest.Checkout.Claim.Path
	tracked := filepath.Join(worktree, "archive-safety.txt")
	if err := os.WriteFile(tracked, []byte("unique local commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, worktree, "add", "archive-safety.txt")
	runGitE2E(t, worktree, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "unique unpushed work")
	if err := os.WriteFile(tracked, []byte("dirty tracked content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(worktree, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("retain this\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitOutput := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	head := gitOutput(worktree, "rev-parse", "HEAD")
	refs := gitOutput(worktree, "for-each-ref", "--format=%(refname) %(objectname)")
	status := gitOutput(worktree, "status", "--porcelain")
	beforeItems := string(runWiE2E(t, binary, repository, env, "list", "--all", "--ids"))
	var result struct {
		Repository model.OwnedRepository `json:"repository"`
		Changed    bool                  `json:"changed"`
	}
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "archive", "test", "--json"), &result)
	if !result.Changed || !result.Repository.Archived {
		t.Fatalf("archive=%+v", result)
	}
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "archive", "test", "--json"), &result)
	if result.Changed {
		t.Fatal("repeated archive changed state")
	}
	assertLists := func() {
		t.Helper()
		var repos []model.OwnedRepository
		decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "list", "--json"), &repos)
		for _, repo := range repos {
			if repo.Name == "test" {
				t.Fatal("archived repository visible by default")
			}
		}
		decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "list", "--all", "--json"), &repos)
		found := false
		for _, repo := range repos {
			if repo.Name == "test" && repo.Archived {
				found = true
			}
		}
		if !found {
			t.Fatal("archived repository missing from --all")
		}
		if text := string(runWiE2E(t, binary, repository, env, "repo", "list", "--all")); !strings.Contains(text, "archived") {
			t.Fatalf("unmarked archive: %s", text)
		}
	}
	assertLists()
	runWiE2E(t, binary, repository, env, "repo", "show", "test")
	for _, mode := range []string{"tui", "headless"} {
		assertWiE2EFails(t, binary, repository, env, "new", "--repo", "test", "--untitled", "--start", "--agent-mode", mode)
	}
	assertWiE2EFails(t, binary, repository, env, "new", "--repo", "test", "--untitled")
	inherited := append(append([]string{}, env...), "WI_ID="+itemID)
	assertWiE2EFails(t, binary, worktree, inherited, "new", "--untitled")
	assertWiE2EFails(t, binary, repository, env, "repo", "add", "test", "--from", repository)
	assertWiE2EFails(t, binary, repository, env, "repo", "remove", "test")
	assertWiE2EFails(t, binary, repository, env, "repo", "delete", "test")
	if items := string(runWiE2E(t, binary, repository, env, "list", "--all", "--ids")); items != beforeItems {
		t.Fatalf("archive/rejected creation changed items: %s", items)
	}
	// Existing items can still be edited and their claims ensured while archived.
	runWiE2E(t, binary, repository, env, "edit", "--item", itemID, "--label", "still-usable")
	runWiE2E(t, binary, repository, env, "workspace", "ensure", "--item", itemID)
	if got := gitOutput(worktree, "rev-parse", "HEAD"); got != head {
		t.Fatal("archive moved unpushed tip")
	}
	if got := gitOutput(worktree, "for-each-ref", "--format=%(refname) %(objectname)"); got != refs {
		t.Fatal("archive changed refs")
	}
	if got := gitOutput(worktree, "status", "--porcelain"); got != status {
		t.Fatal("archive changed dirty worktree")
	}
	for path, want := range map[string]string{tracked: "dirty tracked content\n", untracked: "retain this\n"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("lost %s: %v", path, err)
		}
	}
	stopE2EDaemon(t, binary, repository, env)
	assertLists()
	result.Repository = model.OwnedRepository{}
	decodeE2EJSON(t, runWiE2E(t, binary, repository, env, "repo", "restore", "test", "--json"), &result)
	if !result.Changed || result.Repository.Archived {
		t.Fatalf("restore=%+v", result)
	}
	runWiE2E(t, binary, repository, env, "new", "--repo", "test", "--untitled")
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
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !replaced[key] && !strings.HasPrefix(key, "WI_") && !strings.HasPrefix(key, "PI_") && !strings.HasPrefix(key, "DIRENV_") && key != "TMUX" && key != "TMUX_PANE" {
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
