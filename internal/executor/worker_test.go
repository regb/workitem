package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	executorprotocol "github.com/regb/workitem/executor/protocol"
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
	_, configPath, err := prepareTerminalFiles("master", executorprotocol.TerminalProfile{Shell: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config := string(content)
	for _, want := range []string{"status off", "prefix None", "mouse off", "extended-keys on", "set-clipboard on", "allow-passthrough on", "default-shell '/bin/bash'"} {
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

func TestShellSessionPrefixIdentifiesWholeWorkItemPrefix(t *testing.T) {
	if got := shellSessionPrefix("01M2FWWR70B8EYWDJYVXQYS5Z7"); got != "wi-shell-01m2fwwr70b8-" {
		t.Fatalf("prefix=%q", got)
	}
}

func TestShortKeyIsBoundedAndStable(t *testing.T) {
	first := shortKey(strings.Repeat("master", 30))
	second := shortKey(strings.Repeat("master", 30))
	if first != second || len(first) != 16 {
		t.Fatalf("keys=%q %q", first, second)
	}
}
