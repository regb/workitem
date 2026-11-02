package usage

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func exportJSON(t *testing.T, q Quota) map[string]any {
	t.Helper()
	b, err := json.Marshal(NewSnapshot([]Quota{q}))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 2 || envelope["schema_version"] != float64(1) {
		t.Fatal(string(b))
	}
	return envelope["results"].([]any)[0].(map[string]any)
}

func TestQuotaExportMinimalSuccess(t *testing.T) {
	now := time.Date(2026, 10, 23, 17, 17, 8, 0, time.UTC)
	q := Quota{
		Account: Account{Provider: "codex", ID: "slot"}, State: OK,
		Detail: "Internal diagnostic even on success", CheckedAt: now, NextRefreshAt: &now, ObservedAt: &now,
	}
	result := exportJSON(t, q)
	want := map[string]any{
		"provider": "codex", "account": "slot", "observed_at": "2026-10-23T17:17:08Z", "windows": []any{},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("got %#v; want %#v", result, want)
	}
	if q.Windows != nil {
		t.Fatal("export mutated the internal quota")
	}
}

func TestQuotaExportErrorsAndStaleData(t *testing.T) {
	for _, state := range []State{Unsupported, Unavailable, ExpiredAuth, Stale} {
		t.Run(string(state), func(t *testing.T) {
			q := Quota{Account: Account{Provider: "claude"}, State: state, Detail: "Safe explanation"}
			if state == Stale {
				observed := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
				q.ObservedAt = &observed
				q.Windows = []Window{{ID: "five_hour", RemainingPercent: floatPtr(75)}}
			}
			result := exportJSON(t, q)
			problem := result["error"].(map[string]any)
			if problem["code"] != string(state) || problem["message"] != q.Detail {
				t.Fatal(result)
			}
			if state == Stale {
				if result["stale"] != true || result["observed_at"] == nil || len(result["windows"].([]any)) != 1 {
					t.Fatal(result)
				}
			} else if _, exists := result["stale"]; exists || result["observed_at"] != nil || len(result["windows"].([]any)) != 0 {
				t.Fatal(result)
			}
			if _, exists := result["account"]; exists {
				t.Fatal("empty account should be omitted")
			}
		})
	}
}

func TestQuotaExportDefaultErrorMessages(t *testing.T) {
	for _, state := range []State{Unsupported, Unavailable, ExpiredAuth, Stale, ""} {
		result := exportJSON(t, Quota{State: state})
		problem := result["error"].(map[string]any)
		if problem["message"] == "" || problem["code"] == "" {
			t.Fatal(result)
		}
	}
}

func TestQuotaExportOptionalPermissionCreditsAndWindowScope(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		q := Quota{State: OK, IncludedUsageAllowed: &allowed,
			Credits: &Credits{Remaining: floatPtr(12), Unit: "provider_credits"},
			Windows: []Window{{ID: "weekly", Scope: &WindowScope{Model: "model", Surface: "surface"}}},
		}
		result := exportJSON(t, q)
		if result["included_usage_allowed"] != allowed || result["credits"].(map[string]any)["remaining"] != float64(12) {
			t.Fatal(result)
		}
		window := result["windows"].([]any)[0].(map[string]any)
		if window["remaining_percent"] != nil || window["resets_at"] != nil || window["duration_seconds"] != nil || window["scope"] == nil {
			t.Fatal(window)
		}
	}
}

func TestQuotaInternalPersistenceKeepsRefreshMetadata(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	q := Quota{State: Stale,
		Account: Account{Provider: "codex", ID: "slot"}, Detail: "Retry later", CheckedAt: now, ObservedAt: &now, NextRefreshAt: &now}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"state", "detail", "checked_at", "next_refresh_at"} {
		if !strings.Contains(string(b), `"`+key+`"`) {
			t.Fatalf("persistence lost %s: %s", key, b)
		}
	}
	var decoded Quota
	if err := json.Unmarshal(b, &decoded); err != nil || !reflect.DeepEqual(decoded, q) {
		t.Fatalf("cache round trip: %#v, %v", decoded, err)
	}
}
