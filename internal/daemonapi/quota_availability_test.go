package daemonapi

import (
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestQuotaAvailabilityWaitsForEveryExhaustedWindow(t *testing.T) {
	soon, later := time.Now().Add(time.Hour), time.Now().Add(24*time.Hour)
	window := func(remaining float64, reset time.Time) *v1.UsageWindow {
		return &v1.UsageWindow{RemainingPercent: proto.Float64(remaining), ResetsAt: timestamppb.New(reset)}
	}
	available, reset := quotaAvailability(&v1.UsageQuota{Windows: []*v1.UsageWindow{window(0, soon), window(0, later), window(40, soon)}})
	if available || reset == nil || !reset.Equal(later) {
		t.Fatalf("exhausted: %v %v", available, reset)
	}
	if available, reset := quotaAvailability(&v1.UsageQuota{Windows: []*v1.UsageWindow{window(10, soon), {}}}); !available || reset != nil {
		t.Fatalf("available: %v %v", available, reset)
	}
	if available, _ := quotaAvailability(&v1.UsageQuota{IncludedUsageAllowed: proto.Bool(false)}); available {
		t.Fatal("a refused subscription is available")
	}
}
