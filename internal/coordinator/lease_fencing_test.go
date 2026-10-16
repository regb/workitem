package coordinator

import (
	"github.com/regb/workitem/internal/model"
	"testing"
	"time"
)

func TestLeaseAcquisitionFencesVacantAndStaleSnapshots(t *testing.T) {
	table := newLeaseTable()
	now := time.Now()
	vacant := ""
	a := model.LeaseRequest{ItemID: importTestID, Holder: model.LeaseHolderWeb, ClientID: "a", ExpectedLeaseID: &vacant, TTLSeconds: 45}
	first, err := table.acquire(a, now)
	if err != nil || !first.Granted || first.Lease.ID == "" {
		t.Fatal(first, err)
	}
	b := a
	b.ClientID = "b"
	b.Takeover = true
	if result, err := table.acquire(b, now); err != nil || result.Granted {
		t.Fatal("stale vacant snapshot stole control", result, err)
	}
	expected := first.Lease.ID
	b.ExpectedLeaseID = &expected
	second, err := table.acquire(b, now)
	if err != nil || !second.Granted || second.Lease.ID == first.Lease.ID {
		t.Fatal(second, err)
	}
	a.Takeover = true
	a.ExpectedLeaseID = &expected
	if result, err := table.acquire(a, now); err != nil || result.Granted {
		t.Fatal("confirmed stale snapshot displaced an unexpected holder", result, err)
	}
	if result, err := table.release(model.LeaseRequest{ItemID: importTestID, Holder: model.LeaseHolderWeb, ClientID: "b", ExpectedLeaseID: &expected}); err != nil || result.Granted {
		t.Fatal("stale release removed a newer lease", result, err)
	}
	expected = second.Lease.ID
	b.ExpectedLeaseID = &expected
	renewed, err := table.acquire(b, now.Add(time.Second))
	if err != nil || !renewed.Granted || renewed.Lease.ID != second.Lease.ID {
		t.Fatal("renewal changed lease identity", renewed, err)
	}
}
