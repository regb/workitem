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
	if !strings.Contains(joined, "WI_EXECUTOR_DAEMON_SOCKET="+workerAuthoritySocket(root)) || !strings.Contains(joined, "WI_EXECUTOR=macbook") {
		t.Fatalf("environment=%s", joined)
	}
}

func TestTmuxConfigQuoteDoesNotExposeSingleQuotes(t *testing.T) {
	quoted := tmuxConfigQuote("/tmp/it's bash")
	if quoted != "'/tmp/it'\\''s bash'" {
		t.Fatalf("quoted=%q", quoted)
	}
}

func TestShortKeyIsBoundedAndStable(t *testing.T) {
	first := shortKey(strings.Repeat("master", 30))
	second := shortKey(strings.Repeat("master", 30))
	if first != second || len(first) != 16 {
		t.Fatalf("keys=%q %q", first, second)
	}
}
