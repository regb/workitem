package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
	"github.com/regb/workitem/internal/usage"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolatedTests(m.Run)) }
func commandFixture(t *testing.T, mode string) (*Adapter, string) {
	t.Helper()
	home := t.TempDir()
	report := filepath.Join(home, "methods")
	root := filepath.Join(home, ".claude")
	env := map[string]string{"HOME": home, "GO_WANT_CLAUDE_USAGE_HELPER": "1", "GO_NATIVE_MODE": mode, "GO_NATIVE_REPORT": report, "WI_TEST_PROCESS_ROOT": os.Getenv("WI_TEST_PROCESS_ROOT"), "CLAUDE_CODE_PROJECT_DIR_NAME": "must-not-leak", "CLAUDE_CODE_SESSION_ID": "must-not-leak"}
	return &Adapter{Root: root, Command: []string{os.Args[0], "-test.run=^TestLocalUsageHelper$", "--"}, Environment: env, Now: func() time.Time { return time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC) }}, report
}
func TestLocalUsageHelper(t *testing.T) {
	if os.Getenv("GO_WANT_CLAUDE_USAGE_HELPER") != "1" {
		return
	}
	if os.Getenv("CLAUDE_CODE_PROJECT_DIR_NAME") != "" || os.Getenv("CLAUDE_CODE_SESSION_ID") != "" {
		os.Exit(7)
	}
	if os.Getenv("CLAUDE_CONFIG_DIR") != os.Getenv("GO_EXPECTED_CLAUDE_ROOT") {
		os.Exit(8)
	}
	if os.Getenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC") != os.Getenv("GO_EXPECTED_NATIVE_TRAFFIC_FLAG") {
		os.Exit(9)
	}
	mode := os.Getenv("GO_NATIVE_MODE")
	args := os.Args
	has := func(name, value string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name && args[i+1] == value {
				return true
			}
		}
		return false
	}
	if !has("--setting-sources", "") || !has("--settings", `{"disableAllHooks":true}`) {
		os.Exit(3)
	}
	cwd, _ := os.Getwd()
	if !strings.HasPrefix(filepath.Base(cwd), "wi-native-usage-") {
		os.Exit(4)
	}
	method := "usage"
	if has("auth", "status") {
		method = "auth"
	}
	f, _ := os.OpenFile(os.Getenv("GO_NATIVE_REPORT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	fmt.Fprintln(f, method)
	f.Close()
	if method == "auth" {
		if mode == "autherror" {
			fmt.Fprintln(os.Stderr, "fixture-secret")
			os.Exit(1)
		}
		if mode == "authschema" {
			fmt.Println(`{"loggedIn":"changed"}`)
			os.Exit(0)
		}
		value := map[string]any{"loggedIn": mode != "signedout", "authMethod": "claude.ai", "email": "discard-secret@example.test", "subscriptionType": "team"}
		if mode == "api" {
			value["authMethod"] = "api_key"
		}
		_ = json.NewEncoder(os.Stdout).Encode(value)
		os.Exit(0)
	}
	if !has("--output-format", "stream-json") || !has("-p", "/usage") || !has("--max-turns", "0") || !has("--max-budget-usd", "0.000001") || !has("--tools", "") || !has("--mcp-config", `{"mcpServers":{}}`) {
		os.Exit(5)
	}
	found := false
	for _, arg := range args {
		if arg == "--no-session-persistence" {
			found = true
		}
	}
	if !found {
		os.Exit(6)
	}
	if mode == "hang" {
		time.Sleep(time.Minute)
	}
	if mode == "malformed" {
		fmt.Println(`{"result":`)
		os.Exit(0)
	}
	if mode == "oversize" {
		fmt.Println(strings.Repeat("x", (1<<20)+1))
		os.Exit(0)
	}
	text := "Current session: 9% used · resets Oct 22 at 10pm (Europe/Zurich)\nCurrent week (all models): 19.5% used · resets Oct 23 at 2am (Europe/Zurich)\n96%  Start fresh when you switch tasks\nfixture-secret"
	if mode == "noquota" {
		text = "Total cost: $0.0000\nUsage: 0 input, 0 output"
	}
	value := map[string]any{"type": "result", "local_command": "usage", "num_turns": 0, "duration_api_ms": 0, "total_cost_usd": 0, "is_error": false, "result": text}
	switch mode {
	case "notlocal":
		delete(value, "local_command")
	case "turn":
		value["num_turns"] = 1
	case "cost":
		value["total_cost_usd"] = 0.01
	case "failed":
		value["is_error"] = true
	}
	if mode != "noquota" {
		fmt.Fprintln(os.Stdout, `{"type":"assistant","usage_report":`+structuredFixtureReport+`}`)
	}
	_ = json.NewEncoder(os.Stdout).Encode(value)
	os.Exit(0)
}
func TestLocalNativeQuotaWithoutModelPrompt(t *testing.T) {
	a, report := commandFixture(t, "success")
	q := a.Quota(context.Background())
	if q.State != usage.OK || len(q.Windows) != 4 || q.Windows[0].RemainingPercent == nil || *q.Windows[0].RemainingPercent != 87.25 || q.Windows[1].RemainingPercent == nil || *q.Windows[1].RemainingPercent != 80 || q.Windows[0].ResetsAt == nil {
		t.Fatal(q)
	}
	if !q.Windows[0].ResetsAt.Equal(time.Date(2026, 10, 22, 20, 12, 34, 341312000, time.UTC)) {
		t.Fatal(q.Windows)
	}
	calls, _ := os.ReadFile(report)
	if string(calls) != "auth\nusage\n" {
		t.Fatal(string(calls))
	}
	b, _ := json.Marshal(q)
	if strings.Contains(string(b), "fixture-secret") || strings.Contains(string(b), "discard-secret") {
		t.Fatal("native content leaked")
	}
}
func TestLocalNativeExplicitRootLiteral(t *testing.T) {
	a, _ := commandFixture(t, "success")
	a.Root = filepath.Join(a.Environment["HOME"], "custom-claude")
	a.Environment["CLAUDE_CONFIG_DIR"] = a.Root + "/"
	a.Environment["GO_EXPECTED_CLAUDE_ROOT"] = a.Root + "/"
	if q := a.Quota(context.Background()); q.State != usage.OK {
		t.Fatal(q)
	}
}

func TestLocalNativeQuotaFailures(t *testing.T) {
	for _, mode := range []string{"signedout", "autherror", "authschema", "malformed", "oversize", "noquota", "notlocal", "turn", "cost", "failed", "api"} {
		t.Run(mode, func(t *testing.T) {
			a, report := commandFixture(t, mode)
			q := a.Quota(context.Background())
			want := usage.Unavailable
			if mode == "api" {
				want = usage.Unsupported
			}
			if q.State != want || len(q.Windows) != 0 || q.ObservedAt != nil {
				t.Fatal(q)
			}
			b, _ := json.Marshal(q)
			if strings.Contains(string(b), "fixture-secret") {
				t.Fatal("raw error leaked")
			}
			calls, _ := os.ReadFile(report)
			if mode == "api" && string(calls) != "auth\n" {
				t.Fatal("API authentication queried as a subscription")
			}
		})
	}
}
func TestLocalNativeTimeout(t *testing.T) {
	a, _ := commandFixture(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if q := a.Quota(ctx); q.State != usage.Unavailable {
		t.Fatal(q)
	}
}
