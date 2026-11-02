package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/usage"
)

const structuredFixtureReport = `{"session":{"total_cost_usd":9999,"secret":"fixture-secret","model_usage":{"discard-me":{}}},"rate_limits":{"limits":[{"kind":"session","group":"session","percent":12.75,"resets_at":"2026-10-22T20:12:34.341312Z","severity":"normal","is_active":true},{"kind":"weekly_all","group":"weekly","percent":20,"resets_at":"2026-10-28T20:00:00Z"},{"kind":"weekly_scoped","group":"weekly","percent":50.5,"resets_at":null,"scope":{"model":{"display_name":"Sonnet"}}},{"kind":"weekly_scoped","group":"weekly","percent":100,"resets_at":null,"scope":{"model":{"display_name":"Fable"},"surface":{"display_name":"Claude Code"}}}],"extra_usage":{"used_credits":9999,"secret":"fixture-secret"}}}`

func guardedResult(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "result", "local_command": "usage", "num_turns": 0, "duration_api_ms": 0, "total_cost_usd": 0, "is_error": false, "result": text})
	return string(b)
}
func reportStream(report, result string) []byte {
	return []byte(`{"type":"system","subtype":"init"}` + "\n" + `{"type":"assistant","usage_report":` + report + `}` + "\n" + result + "\n")
}

func TestStructuredNativeTeamQuota(t *testing.T) {
	a, _ := commandFixture(t, "structured")
	q := a.Quota(context.Background())
	if q.State != usage.OK || len(q.Windows) != 4 || q.Windows[0].RemainingPercent == nil || *q.Windows[0].RemainingPercent != 87.25 {
		t.Fatal(q)
	}
	if !q.Windows[0].ResetsAt.Equal(time.Date(2026, 10, 22, 20, 12, 34, 341312000, time.UTC)) {
		t.Fatal("lost exact native reset", q.Windows[0])
	}
	if q.Windows[2].ID != "seven_day_sonnet" || q.Windows[3].Scope == nil || q.Windows[3].Scope.Model != "Fable" || q.Windows[3].Scope.Surface != "Claude Code" || *q.Windows[3].RemainingPercent != 0 {
		t.Fatal(q.Windows)
	}
	b, _ := json.Marshal(q)
	for _, private := range []string{"fixture-secret", "discard-me", "9999", "total_cost_usd", "model_usage", "used_credits"} {
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
