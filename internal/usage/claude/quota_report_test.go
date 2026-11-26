package claude

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/usage"
)

// The report's shape follows Claude Code's stream-json usage_report; its
// session and extra_usage sections hold data that must never be exported.
//
//go:embed testdata/structured-usage-report.json
var structuredFixtureJSON []byte

// structuredFixtureReport is the report compacted to one stream-json line.
var structuredFixtureReport = func() string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, structuredFixtureJSON); err != nil {
		panic(err)
	}
	return compact.String()
}()

func guardedResult(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "result", "local_command": "usage", "num_turns": 0, "duration_api_ms": 0, "total_cost_usd": 0, "is_error": false, "result": text})
	return string(b)
}
func reportStream(report, result string) []byte {
	return []byte(`{"type":"system","subtype":"init"}` + "\n" + `{"type":"assistant","usage_report":` + report + `}` + "\n" + result + "\n")
}

// The quota comes from auth status and the local /usage command, never a
// model prompt, and keeps only the native rate limits.
func TestStructuredNativeTeamQuota(t *testing.T) {
	a, report := commandFixture(t, "structured")
	q := a.Quota(context.Background())
	if q.State != usage.OK || len(q.Windows) != 4 || q.Windows[0].RemainingPercent == nil || *q.Windows[0].RemainingPercent != 87.25 || q.Windows[1].RemainingPercent == nil || *q.Windows[1].RemainingPercent != 80 {
		t.Fatal(q)
	}
	if calls, _ := os.ReadFile(report); string(calls) != "auth\nusage\n" {
		t.Fatal(string(calls))
	}
	if !q.Windows[0].ResetsAt.Equal(time.Date(2026, 10, 22, 20, 12, 34, 341312000, time.UTC)) {
		t.Fatal("lost exact native reset", q.Windows[0])
	}
	if q.Windows[2].ID != "seven_day_sonnet" || q.Windows[3].Scope == nil || q.Windows[3].Scope.Model != "Fable" || q.Windows[3].Scope.Surface != "Claude Code" || *q.Windows[3].RemainingPercent != 0 {
		t.Fatal(q.Windows)
	}
	b, _ := json.Marshal(q)
	for _, private := range []string{"fixture-secret", "discard-secret", "discard-me", "9999", "total_cost_usd", "model_usage", "used_credits"} {
		if strings.Contains(string(b), private) {
			t.Fatal("non-quota native data exported", string(b))
		}
	}
}
func TestNativeQuotaPreservesTrafficPolicy(t *testing.T) {
	for _, flag := range []string{"", "0", "1", "true"} {
		t.Run("flag="+flag, func(t *testing.T) {
			mode := "structured"
			if flag == "1" || flag == "true" {
				mode = "noquota"
			}
			a, _ := commandFixture(t, mode)
			if flag != "" {
				a.Environment["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = flag
				a.Environment["GO_EXPECTED_NATIVE_TRAFFIC_FLAG"] = flag
			}
			q := a.Quota(context.Background())
			if mode == "structured" {
				if q.State != usage.OK {
					t.Fatal("default metadata request suppressed", q)
				}
			} else if q.State != usage.Unavailable || !strings.Contains(q.Detail, "native traffic restriction") {
				t.Fatal("caller policy ignored", q)
			}
		})
	}
}

func TestStructuredQuotaMissingIsNotExhaustion(t *testing.T) {
	legacyText := "Current session: 0% used"
	for _, report := range []string{`null`, `{"rate_limits":null}`, `{"rate_limits":{"limits":null}}`, `{"rate_limits":{"limits":[]}}`, `{"rate_limits":{"limits":[{"kind":"session","resets_at":null}]}}`} {
		windows, err := decodeLocalUsage(reportStream(report, guardedResult(legacyText)))
		if !errors.Is(err, errNoNativeQuota) || len(windows) != 0 {
			t.Fatal("missing native limits replaced by a guessed value", windows, err)
		}
	}
}
func TestStructuredQuotaRequiresZeroTurnResult(t *testing.T) {
	good := guardedResult("irrelevant")
	for _, result := range []string{strings.Replace(good, `"num_turns":0`, `"num_turns":1`, 1), strings.Replace(good, `"total_cost_usd":0`, `"total_cost_usd":1`, 1), strings.Replace(good, `"duration_api_ms":0`, `"duration_api_ms":1`, 1), strings.Replace(good, `"local_command":"usage"`, `"local_command":"other"`, 1), ""} {
		windows, err := decodeLocalUsage(reportStream(structuredFixtureReport, result))
		if err == nil || len(windows) != 0 {
			t.Fatal("published before result validation", windows, err)
		}
	}
	if _, err := decodeLocalUsage(append(reportStream(structuredFixtureReport, good), []byte(good)...)); err == nil {
		t.Fatal("multiple results accepted")
	}
}
func TestStructuredQuotaRequiresStructuredReport(t *testing.T) {
	for _, report := range []string{`{"rate_limits":"changed"}`, `{"rate_limits":{"limits":[{"kind":"session","percent":"changed"}]}}`} {
		if _, err := decodeLocalUsage(reportStream(report, guardedResult("Current session: 0% used"))); err == nil {
			t.Fatal("schema change accepted")
		}
	}
	windows, err := decodeLocalUsage([]byte(guardedResult("Current session: 25% used")))
	if !errors.Is(err, errNoNativeQuota) || len(windows) != 0 {
		t.Fatal(windows, err)
	}
	a, _ := commandFixture(t, "noquota")
	q := a.Quota(context.Background())
	if !strings.Contains(q.Detail, "Team login") || !strings.Contains(q.Detail, "unknown, not exhausted") {
		t.Fatal(q)
	}
}
