package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
	"github.com/regb/workitem/internal/usage"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolatedTests(m.Run)) }

var now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
var bindingA = strings.Repeat("a", 64)
var bindingB = strings.Repeat("b", 64)

func reply(binding string) bridgeReply {
	observed := now
	reset := now.Add(24 * time.Hour)
	r := bridgeReply{State: usage.OK, Binding: binding, ObservedAt: &observed}
	for _, id := range []string{"five_hour", "weekly", "monthly"} {
		percent := 75.25
		r.Windows = append(r.Windows, usage.Window{ID: id, RemainingPercent: &percent, ResetsAt: &reset})
	}
	return r
}
func encoded(r bridgeReply) []byte { b, _ := json.Marshal(map[string]any{"output": r}); return b }
func fixtureAdapter(t *testing.T) (*Adapter, *atomic.Int32, *string, *bridgeReply) {
	t.Helper()
	calls := new(atomic.Int32)
	binding := bindingA
	response := reply(binding)
	a := &Adapter{Root: t.TempDir(), Now: func() time.Time { return now }, Command: []string{"fixture"}, Environment: map[string]string{"OPENCODE_CONFIG_CONTENT": "caller-owned-inline-config"}}
	a.run = func(ctx context.Context, command, args []string, env map[string]string, files map[string][]byte) ([]byte, error) {
		if env["OPENCODE_CONFIG_CONTENT"] != "caller-owned-inline-config" || string(files[bridgePath]) != string(bridge) {
			t.Fatal("caller config or bundled helper changed")
		}
		if len(args) != 8 || args[0] != "api" || args[1] != "--standalone" || args[4] != "POST" || args[6] != "--data" {
			t.Fatalf("unsafe arguments: %v", args)
		}
		if args[5] == "/api/rpc/wi.quota/identity" {
			return encoded(bridgeReply{State: usage.OK, Binding: binding}), nil
		}
		// Native command has no credential arguments; the only request input is a binding.
		var input struct {
			Input struct {
				Binding string `json:"binding"`
			} `json:"input"`
		}
		if json.Unmarshal([]byte(args[7]), &input) != nil || input.Input.Binding != binding {
			t.Fatal("binding was not sent")
		}
		calls.Add(1)
		return encoded(response), nil
	}
	return a, calls, &binding, &response
}

func TestNativeOpenCodeIdentityBeforeCacheAndSwitch(t *testing.T) {
	a, calls, binding, response := fixtureAdapter(t)
	cache := usage.Cache{Root: t.TempDir(), Now: a.Now}
	p := a.ResolveAccount(context.Background())
	firstID := p.Account()
	q, err := cache.Get(context.Background(), p, false)
	if err != nil || q.State != usage.OK || len(q.Windows) != 3 || calls.Load() != 1 {
		t.Fatal(q, err, calls.Load())
	}
	q, err = cache.Get(context.Background(), a.ResolveAccount(context.Background()), false)
	if err != nil || q.State != usage.OK || calls.Load() != 1 {
		t.Fatal(q, err)
	}
	*binding = bindingB
	*response = reply(bindingB)
	p = a.ResolveAccount(context.Background())
	if p.Account() == firstID {
		t.Fatal("account switch reused cache identity")
	}
	q, err = cache.Get(context.Background(), p, false)
	if err != nil || q.State != usage.OK || calls.Load() != 2 {
		t.Fatal(q, err, calls.Load())
	}
}

func TestCacheResolvesUnboundOpenCodeAdapter(t *testing.T) {
	a, calls, _, _ := fixtureAdapter(t)
	cache := usage.Cache{Root: t.TempDir(), Now: a.Now}
	for i := 0; i < 2; i++ {
		q, err := cache.Get(context.Background(), a, false)
		if err != nil || q.State != usage.OK || q.Account == a.Account() {
			t.Fatal(q, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("cache did not use resolved identity")
	}
}

func TestNativeOpenCodeCachedIdentityRecheck(t *testing.T) {
	a, calls, binding, _ := fixtureAdapter(t)
	cache := usage.Cache{Root: t.TempDir(), Now: a.Now}
	p := a.ResolveAccount(context.Background())
	if q, err := cache.Get(context.Background(), p, false); err != nil || q.State != usage.OK {
		t.Fatal(q, err)
	}
	*binding = bindingB
	q, err := cache.Get(context.Background(), p, false)
	if err != nil || q.State != usage.Unavailable || q.ObservedAt != nil || len(q.Windows) != 0 || calls.Load() != 1 {
		t.Fatal(q, err)
	}
}

func TestNativeOpenCodeRejectsUnsafeReplies(t *testing.T) {
	for _, mode := range []string{"wrong_binding", "missing_windows", "missing_observation", "bad_percent", "future_observation", "unknown_state", "identity_changed", "paused", "schema"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _, r := fixtureAdapter(t)
			switch mode {
			case "wrong_binding":
				r.Binding = bindingB
			case "missing_windows":
				r.Windows = nil
			case "missing_observation":
				r.ObservedAt = nil
			case "bad_percent":
				v := 101.
				r.Windows[0].RemainingPercent = &v
			case "future_observation":
				v := now.Add(time.Hour)
				r.ObservedAt = &v
			case "unknown_state":
				r.State = "changed"
			case "identity_changed":
				*r = bridgeReply{State: usage.Unavailable, Reason: "account_changed"}
			case "paused":
				*r = bridgeReply{State: usage.Unavailable, Binding: bindingA, Reason: "access_paused"}
			case "schema":
				*r = bridgeReply{State: usage.Unavailable, Binding: bindingA, Reason: "schema_changed"}
			}
			q := a.Quota(context.Background())
			if q.State == usage.OK || len(q.Windows) != 0 || q.ObservedAt != nil || !q.PreventStaleFallback {
				t.Fatal(q)
			}
		})
	}
}
func TestNativeOpenCodeRateLimitAndSafeDiagnostics(t *testing.T) {
	a, _, _, r := fixtureAdapter(t)
	seconds := int64(900)
	*r = bridgeReply{State: usage.Unavailable, Reason: "rate_limited", Binding: bindingA, RetryAfterSeconds: &seconds}
	q := a.Quota(context.Background())
	if q.State != usage.Unavailable || q.NextRefreshAt == nil || !q.NextRefreshAt.Equal(now.Add(15*time.Minute)) || q.PreventStaleFallback {
		t.Fatal(q)
	}
	*r = bridgeReply{State: usage.Unavailable, Reason: "fixture-secret-in-native-error"}
	q = a.Quota(context.Background())
	b, _ := json.Marshal(usage.NewSnapshot([]usage.Quota{q}))
	if strings.Contains(string(b), "fixture-secret") {
		t.Fatal(string(b))
	}
}
func TestNativeOpenCodeIdentityFailureNeverUsesOldSlot(t *testing.T) {
	a, _, _, _ := fixtureAdapter(t)
	a.run = func(context.Context, []string, []string, map[string]string, map[string][]byte) ([]byte, error) {
		return []byte(`{"output":{"state":"unsupported","reason":"oauth_required"}}`), nil
	}
	q := a.Quota(context.Background())
	if q.State != usage.Unsupported || q.Account != a.Account() || !q.PreventStaleFallback || len(q.Windows) != 0 {
		t.Fatal(q)
	}
}

func TestNativeOpenCodeProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_OC_HELPER") != "1" {
		return
	}
	cwd, _ := os.Getwd()
	if !strings.HasPrefix(filepath.Base(cwd), "wi-native-usage-") {
		os.Exit(4)
	}
	if _, err := os.Stat(filepath.Join(cwd, bridgePath)); err != nil {
		os.Exit(5)
	}
	if os.Getenv("OPENCODE_CONFIG_CONTENT") != "preserved" {
		os.Exit(6)
	}
	mode := os.Getenv("GO_OC_MODE")
	if mode == "hang" {
		time.Sleep(time.Minute)
	}
	if mode == "failure" {
		fmt.Fprintln(os.Stderr, "fixture-never-output-secret")
		os.Exit(1)
	}
	if mode == "oversize" {
		fmt.Print(strings.Repeat("x", (1<<20)+1))
		os.Exit(0)
	}
	if mode == "malformed" {
		fmt.Print(`{"output":`)
		os.Exit(0)
	}
	args := strings.Join(os.Args, " ")
	if strings.Contains(args, "/identity") {
		fmt.Print(string(encoded(bridgeReply{State: usage.OK, Binding: bindingA})))
	} else {
		fmt.Print(string(encoded(reply(bindingA))))
	}
	os.Exit(0)
}
func processAdapter(t *testing.T, mode string) *Adapter {
	t.Helper()
	return &Adapter{Root: t.TempDir(), Command: []string{os.Args[0], "-test.run=^TestNativeOpenCodeProcessHelper$", "--"}, Now: func() time.Time { return now }, Environment: map[string]string{
		"HOME": t.TempDir(), "GO_WANT_OC_HELPER": "1", "GO_OC_MODE": mode, "OPENCODE_CONFIG_CONTENT": "preserved", "WI_TEST_PROCESS_ROOT": os.Getenv("WI_TEST_PROCESS_ROOT"),
	}}
}
func TestNativeOpenCodeBridgeJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; run node --test bridge/bridge.test.mjs separately")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test", "bridge/bridge.test.mjs")
	cmd.Env = append(os.Environ(), "NODE_OPTIONS=")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mocked native bridge tests: %v\n%s", err, output)
	}
}

func TestNativeOpenCodeBoundedProcess(t *testing.T) {
	q := processAdapter(t, "success").Quota(context.Background())
	if q.State != usage.OK || len(q.Windows) != 3 {
		t.Fatal(q)
	}
	for _, mode := range []string{"failure", "oversize", "malformed"} {
		q = processAdapter(t, mode).Quota(context.Background())
		b, _ := json.Marshal(q)
		if q.State != usage.Unavailable || len(q.Windows) != 0 || strings.Contains(string(b), "fixture-never-output-secret") {
			t.Fatal(q)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if q = processAdapter(t, "hang").Quota(ctx); q.State != usage.Unavailable {
		t.Fatal(q)
	}
}
