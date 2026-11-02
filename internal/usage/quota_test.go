package usage

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestRemainingQuotaConversion(t *testing.T) {
	for _, tc := range []struct{ nativeUsed, remaining float64 }{{0, 100}, {25.5, 74.5}, {100, 0}, {125, 0}} {
		w := NativeWindow("five_hour", &tc.nativeUsed, nil, nil)
		if w == nil || w.RemainingPercent == nil || *w.RemainingPercent != tc.remaining {
			t.Fatalf("native utilization %g: %+v", tc.nativeUsed, w)
		}
	}
	for _, value := range []*float64{nil, floatPtr(-1), floatPtr(math.NaN()), floatPtr(math.Inf(1))} {
		if NativeWindow("unknown", value, nil, nil) != nil {
			t.Fatal("unknown native quota must not become exhausted or unlimited")
		}
	}
}
func TestRemainingQuotaExportOnly(t *testing.T) {
	snapshot := NewSnapshot([]Quota{{Account: Account{Provider: "claude", ID: "fixture"}, State: OK, Windows: []Window{{ID: "five_hour", RemainingPercent: floatPtr(75)}, {ID: "unknown", RemainingPercent: nil}}, Credits: &Credits{Remaining: floatPtr(12), Unit: "provider_credits"}}})
	b, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"used_percent", "tokens", "history", "cost", "session", "workspace", `"state"`, `"detail"`, `"source"`, `"refresh_mode"`, `"authentication"`, `"checked_at"`, `"next_refresh_at"`, `"scope"`} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("not a quota-only export: %s", b)
		}
	}
	for _, required := range []string{`"schema_version":1`, `"provider":"claude"`, `"account":"fixture"`, `"remaining_percent":75`, `"remaining_percent":null`, `"remaining":12`} {
		if !strings.Contains(string(b), required) {
			t.Fatalf("missing %s in %s", required, b)
		}
	}
	b, _ = json.Marshal(NewSnapshot(nil))
	if !strings.Contains(string(b), `"results":[]`) {
		t.Fatal(string(b))
	}
}
