package codex

import (
	"bufio"
	"context"
	_ "embed"
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

// Shape verified against the installed Codex 0.160.0 generated schema; values are synthetic.
//
//go:embed testdata/0.160.0-rate-limits.json
var modernRateLimits json.RawMessage

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolatedTests(m.Run)) }
func fixture(t *testing.T, mode string) (*Adapter, string) {
	t.Helper()
	root := t.TempDir()
	report := filepath.Join(root, "methods")
	env := map[string]string{"HOME": root, "GO_WANT_USAGE_HELPER": "1", "GO_NATIVE_MODE": mode, "GO_NATIVE_REPORT": report, "WI_TEST_PROCESS_ROOT": os.Getenv("WI_TEST_PROCESS_ROOT")}
	return &Adapter{Root: root, Environment: env, Command: []string{os.Args[0], "-test.run=^TestNativeHelper$"}, Now: func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }}, report
}
func TestNativeHelper(t *testing.T) {
	if os.Getenv("GO_WANT_USAGE_HELPER") != "1" {
		return
	}
	mode := os.Getenv("GO_NATIVE_MODE")
	scanner := bufio.NewScanner(os.Stdin)
	send := func(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
	for scanner.Scan() {
		var request struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		f, _ := os.OpenFile(os.Getenv("GO_NATIVE_REPORT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		fmt.Fprintln(f, request.Method)
		f.Close()
		switch request.Method {
		case "initialize":
			if mode == "hang" {
				time.Sleep(time.Minute)
			}
			send(map[string]any{"id": request.ID, "result": map[string]any{"userAgent": "fixture"}})
		case "initialized":
		case "account/read":
			if string(request.Params) != `{"refreshToken":false}` {
				os.Exit(4)
			}
			var account any = map[string]string{"type": "chatgpt", "email": "discard-secret@example.test"}
			if mode == "api" {
				account = map[string]string{"type": "apiKey"}
			}
			if mode == "signedout" {
				account = nil
			}
			result := map[string]any{"account": account}
			if mode == "mixed" {
				result["requiresOpenaiAuth"] = false
			}
			send(map[string]any{"id": request.ID, "result": result})
		case "account/rateLimits/read":
			var params struct {
				Exclude *bool `json:"excludeResetCreditDetails"`
				Reserve *bool `json:"supportsLunaReserve"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.Exclude == nil || !*params.Exclude || params.Reserve == nil || *params.Reserve {
				os.Exit(7)
			}
			if mode == "params" {
				send(map[string]any{"id": request.ID, "error": map[string]any{"code": -32602, "message": "fixture-secret"}})
				continue
			}
			if mode == "modern" {
				send(map[string]any{"id": request.ID, "result": modernRateLimits})
				continue
			}
			if mode == "401" || mode == "429" {
				code := 401
				if mode == "429" {
					code = 429
				}
				send(map[string]any{"id": request.ID, "error": map[string]any{"code": code, "message": "fixture-secret", "data": "fixture-secret"}})
				continue
			}
			if mode == "malformed" {
				fmt.Println(`{"broken":`)
				continue
			}
			if mode == "unknown" {
				send(map[string]any{"id": request.ID, "result": map[string]string{"changed": "fixture-secret"}})
				continue
			}
			if mode == "schema" {
				send(map[string]any{"id": request.ID, "result": map[string]string{"rateLimits": "changed"}})
				continue
			}
			if mode == "oversize" {
				fmt.Println(strings.Repeat("x", (1<<20)+1))
				continue
			}
			if mode == "credentials-request" {
				send(map[string]any{"id": 77, "method": "account/chatgptAuthTokens/refresh"})
				if !scanner.Scan() || !strings.Contains(scanner.Text(), `"error"`) || strings.Contains(scanner.Text(), `accessToken`) {
					os.Exit(5)
				}
			}
			single := map[string]any{"primary": map[string]any{"usedPercent": 25.5, "windowDurationMins": 300, "resetsAt": 1792700000}, "credits": map[string]any{"balance": "12.25", "unlimited": false}}
			if mode == "unknowncredits" {
				single = map[string]any{"credits": map[string]any{"unlimited": false}}
			}
			if mode == "zerocredits" {
				single = map[string]any{"credits": map[string]any{"unlimited": false, "balance": "0"}}
			}
			result := map[string]any{"rateLimits": single}
			if mode == "blocked" {
				result["ordinaryUsageAllowed"] = false
			}
			if mode == "multi" {
				result["rateLimitsByLimitId"] = map[string]any{"codex": single, "codex_other": map[string]any{"secondary": map[string]any{"usedPercent": 40}}}
			}
			send(map[string]any{"method": "account/rateLimits/updated", "params": map[string]string{"discard": "fixture-secret"}})
			send(map[string]any{"id": request.ID, "result": result})
		default:
			os.Exit(6) // No thread, turn, login, token, or model requests are allowed.
		}
	}
	os.Exit(0)
}
func TestNativeQuotaNoModelTurnOrCredentialRead(t *testing.T) {
	for _, mode := range []string{"single", "multi", "credentials-request"} {
		t.Run(mode, func(t *testing.T) {
			a, report := fixture(t, mode)
			if err := os.WriteFile(filepath.Join(a.Root, "auth.json"), []byte("not a valid credential document"), 0600); err != nil {
				t.Fatal(err)
			}
			q := a.Quota(context.Background())
			if q.State != usage.OK || len(q.Windows) == 0 || q.Windows[0].RemainingPercent == nil || *q.Windows[0].RemainingPercent != 74.5 || q.ObservedAt == nil {
				t.Fatal(q)
			}
			if mode == "multi" && (len(q.Windows) != 2 || q.Windows[1].ID != "codex_other/secondary") {
				t.Fatal("double-counted legacy snapshot", q)
			}
			if q.Credits == nil || q.Credits.Remaining == nil || *q.Credits.Remaining != 12.25 {
				t.Fatal(q)
			}
			methods, _ := os.ReadFile(report)
			if strings.Contains(string(methods), "turn/") || strings.Contains(string(methods), "thread/") {
				t.Fatal(string(methods))
			}
			b, _ := json.Marshal(q)
			if strings.Contains(string(b), "fixture-secret") || strings.Contains(string(b), "discard-secret") {
				t.Fatal("native content leaked")
			}
			auth, _ := os.ReadFile(filepath.Join(a.Root, "auth.json"))
			if string(auth) != "not a valid credential document" {
				t.Fatal("credentials touched")
			}
		})
	}
}
func TestNativeQuotaFailuresAndFunding(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		state usage.State
	}{{"api", usage.Unsupported}, {"params", usage.Unsupported}, {"mixed", usage.Unsupported}, {"signedout", usage.Unavailable}, {"401", usage.ExpiredAuth}, {"429", usage.Unavailable}, {"malformed", usage.Unavailable}, {"unknown", usage.Unavailable}, {"schema", usage.Unavailable}, {"oversize", usage.Unavailable}, {"unknowncredits", usage.Unavailable}} {
		t.Run(tc.mode, func(t *testing.T) {
			a, report := fixture(t, tc.mode)
			q := a.Quota(context.Background())
			if q.State != tc.state || q.ObservedAt != nil || len(q.Windows) != 0 {
				t.Fatal(q)
			}
			b, _ := json.Marshal(q)
			if strings.Contains(string(b), "fixture-secret") {
				t.Fatal("error leaked")
			}
			methods, _ := os.ReadFile(report)
			if (tc.mode == "api" || tc.mode == "mixed") && strings.Contains(string(methods), "account/rateLimits/read") {
				t.Fatal("API login used for subscription query")
			}
		})
	}
}
func TestInstalledProtocolRemainingQuota(t *testing.T) {
	a, report := fixture(t, "modern")
	q := a.Quota(context.Background())
	if q.State != usage.OK || len(q.Windows) != 3 || *q.Windows[0].RemainingPercent != 95 || *q.Windows[1].RemainingPercent != 93 || *q.Windows[2].RemainingPercent != 20 {
		t.Fatal(q)
	}
	if q.Windows[0].ID != "codex/primary" || q.Windows[2].ID != "codex_code_review/secondary" || *q.Windows[0].DurationSeconds != 18000 || *q.Windows[1].DurationSeconds != 604800 {
		t.Fatal(q.Windows)
	}
	if q.IncludedUsageAllowed == nil || !*q.IncludedUsageAllowed {
		t.Fatal("lost native permission", q)
	}
	b, _ := json.Marshal(usage.NewSnapshot([]usage.Quota{q}))
	for _, secret := range []string{"fixture-secret", "fixture-account-not-for-export", "rateLimitResetCredits", "rateLimitUpsell", "usedPercent"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("private or non-remaining native data exported", string(b))
		}
	}
	methods, _ := os.ReadFile(report)
	if string(methods) != "initialize\ninitialized\naccount/read\naccount/rateLimits/read\n" {
		t.Fatal("unexpected native operation", string(methods))
	}
}
func TestNativePermissionNeverInferredFromRemaining(t *testing.T) {
	a, _ := fixture(t, "blocked")
	q := a.Quota(context.Background())
	if q.State != usage.OK || q.IncludedUsageAllowed == nil || *q.IncludedUsageAllowed || *q.Windows[0].RemainingPercent != 74.5 {
		t.Fatal(q)
	}
	later := usage.AgeObservation(q, a.now().Add(14*24*time.Hour))
	if later.IncludedUsageAllowed == nil || *later.IncludedUsageAllowed {
		t.Fatal("inferred permission after a reset", later)
	}
	a, _ = fixture(t, "single")
	q = a.Quota(context.Background())
	if q.IncludedUsageAllowed != nil {
		t.Fatal("invented permission from percentages", q)
	}
}

func TestNativeZeroRemainingCreditsIsKnown(t *testing.T) {
	a, _ := fixture(t, "zerocredits")
	q := a.Quota(context.Background())
	if q.State != usage.OK || q.Credits == nil || q.Credits.Remaining == nil || *q.Credits.Remaining != 0 || len(q.Windows) != 0 {
		t.Fatal(q)
	}
}

func TestUnknownBucketIDsAreStableAndOpaque(t *testing.T) {
	var one, two usage.Quota
	if err := decodeLimits([]byte(`{"rateLimitsByLimitId":{"private-native-label":{"primary":{"usedPercent":10}}}}`), &one); err != nil {
		t.Fatal(err)
	}
	if err := decodeLimits([]byte(`{"rateLimitsByLimitId":{"another-label":{"primary":{"usedPercent":20}},"private-native-label":{"primary":{"usedPercent":10}}}}`), &two); err != nil {
		t.Fatal(err)
	}
	if len(one.Windows) != 1 || len(two.Windows) != 2 || one.Windows[0].ID != two.Windows[1].ID {
		t.Fatal(one, two)
	}
	if strings.Contains(one.Windows[0].ID, "private") {
		t.Fatal("raw native label leaked")
	}
}

func TestNativeQuotaTimeout(t *testing.T) {
	a, _ := fixture(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	q := a.Quota(ctx)
	if q.State != usage.Unavailable || time.Since(started) > 2*time.Second {
		t.Fatal(q)
	}
}
