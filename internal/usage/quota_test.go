package usage

import (
	"math"
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
