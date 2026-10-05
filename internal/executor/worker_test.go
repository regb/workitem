package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	"github.com/regb/workitem/internal/coordinator"
)

func TestValidateMasterIDRejectsPathTraversal(t *testing.T) {
	for _, value := range []string{"", ".", "..", "../master", "a/b", "a\\b", "a\nmaster"} {
		if err := validateMasterID(value); err == nil {
			t.Errorf("validateMasterID(%q) accepted", value)
		}
	}
	if err := validateMasterID("49547007995620ef"); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityInfoPersistsWorkerRoutingContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	response, err := (Worker{}).authorityInfo(executorprotocol.Request{MasterID: "master", ExecutorName: "macbook"})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".local", "share", "wi-executor", shortKey("master"))
	if response.Authority == nil || response.Authority.SocketPath != workerAuthoritySocket(root) {
		t.Fatalf("response=%+v", response)
	}
	if _, err := os.Stat(filepath.Join(root, "authority.json")); err != nil {
		t.Fatal(err)
	}
	environment := workerEnvironment(root, "master")
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "WI_EXECUTOR_DAEMON_SOCKET="+workerAuthoritySocket(root)) || !strings.Contains(joined, "WI_EXECUTOR=macbook") || !strings.Contains(joined, "WI_EXECUTOR_WORKER=1") {
		t.Fatalf("environment=%s", joined)
	}
}

func TestPrepareTerminalFilesUsesPrivatePersistenceTmuxConfiguration(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	_, configPath, err := prepareTerminalFiles("master", executorprotocol.TerminalProfile{Shell: "/bin/bash", ModeKeys: "vi"})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config := string(content)
	for _, want := range []string{"status off", "prefix None", "mouse off", "history-limit 50000", "extended-keys on", "set-clipboard on", "allow-passthrough on", "mode-keys vi", "default-shell '/bin/bash'"} {
		if !strings.Contains(config, want) {
			t.Fatalf("config %q does not contain %q", config, want)
		}
	}
}

func TestTmuxConfigQuoteDoesNotExposeSingleQuotes(t *testing.T) {
	quoted := tmuxConfigQuote("/tmp/it's bash")
	if quoted != "'/tmp/it'\\''s bash'" {
		t.Fatalf("quoted=%q", quoted)
	}
}

func TestShellSessionNameIsStablePerGatewayPane(t *testing.T) {
	itemID := "01M2FWWR70B8EYWDJYVXQYS5Z7"
	first := shellSessionName(itemID, "%42")
	if first != shellSessionName(itemID, "%42") || first == shellSessionName(itemID, "%43") || !strings.HasPrefix(first, shellSessionPrefix(itemID)) {
		t.Fatalf("first=%q other=%q", first, shellSessionName(itemID, "%43"))
	}
	if !validGatewayPaneID("%42") || validGatewayPaneID("42") || validGatewayPaneID("%bad") {
		t.Fatal("gateway pane validation mismatch")
	}
}

func TestShellSessionPrefixIdentifiesWholeWorkItemPrefix(t *testing.T) {
	if got := shellSessionPrefix("01M2FWWR70B8EYWDJYVXQYS5Z7"); got != "wi-shell-01m2fwwr70b8-" {
		t.Fatalf("prefix=%q", got)
	}
}

func TestExecutorRuntimeReadyUsesReadyEventInsteadOfPromptCapability(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"runtime_id": "runtime-1"})
	events, _ := json.Marshal(coordinator.RuntimeEventsResult{Events: []coordinator.DomainEvent{{Type: "runtime.ready", Payload: payload}}})
	if !executorRuntimeReady(events, "runtime-1") {
		t.Fatal("ready event was not recognized")
	}
	if executorRuntimeReady(events, "runtime-2") {
		t.Fatal("ready event from another runtime was accepted")
	}
}

func TestImportCoordinatorBundleDoesNotRewriteWorkerBranches(t *testing.T) {
	root := t.TempDir()
	source, repository, bundle := filepath.Join(root, "source"), filepath.Join(root, "worker.git"), filepath.Join(root, "coordinator.bundle")
	runGitTest(t, "init", "-q", "-b", "main", source)
	runGitTest(t, "-C", source, "config", "user.email", "test@example.com")
	runGitTest(t, "-C", source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", source, "add", "file")
	runGitTest(t, "-C", source, "commit", "-qm", "one")
	base := strings.TrimSpace(runGitTest(t, "-C", source, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", source, "commit", "-qam", "two")
	ahead := strings.TrimSpace(runGitTest(t, "-C", source, "rev-parse", "HEAD"))
	runGitTest(t, "clone", "-q", "--bare", source, repository)
	runGitTest(t, "-C", repository, "update-ref", "refs/heads/wi/example", ahead)
	runGitTest(t, "-C", source, "branch", "wi/example", base)
	runGitTest(t, "-C", source, "bundle", "create", bundle, "--all")
	if err := importCoordinatorBundle(context.Background(), repository, bundle); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGitTest(t, "-C", repository, "rev-parse", "refs/heads/wi/example")); got != ahead {
		t.Fatalf("worker branch=%s want %s", got, ahead)
	}
	if got := strings.TrimSpace(runGitTest(t, "-C", repository, "rev-parse", "refs/remotes/wi-coordinator/wi/example")); got != base {
		t.Fatalf("coordinator branch=%s want %s", got, base)
	}
}

func runGitTest(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, output, err)
	}
	return string(output)
}

func TestShortKeyIsBoundedAndStable(t *testing.T) {
	first := shortKey(strings.Repeat("master", 30))
	second := shortKey(strings.Repeat("master", 30))
	if first != second || len(first) != 16 {
		t.Fatalf("keys=%q %q", first, second)
	}
}
