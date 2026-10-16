package coordinator

import (
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestLeaseTableGrantsVacantRenewsOwnAndRequiresTakeover(t *testing.T) {
	table := newLeaseTable()
	now := time.Now().UTC()
	web := model.LeaseRequest{ItemID: importTestID, Holder: model.LeaseHolderWeb, ClientID: "tab-a", TTLSeconds: 30}
	first, err := table.acquire(web, now)
	if err != nil || !first.Granted || first.Lease.ExpiresAt == nil || first.Previous != nil {
		t.Fatalf("vacant acquire=%+v err=%v", first, err)
	}
	renewed, err := table.acquire(web, now.Add(10*time.Second))
	if err != nil || !renewed.Granted || !renewed.Lease.AcquiredAt.Equal(now) || !renewed.Lease.ExpiresAt.After(*first.Lease.ExpiresAt) {
		t.Fatalf("renewal=%+v err=%v", renewed, err)
	}
	terminal := model.LeaseRequest{ItemID: importTestID, Holder: model.LeaseHolderTerminal, ClientID: "terminal"}
	refused, err := table.acquire(terminal, now.Add(11*time.Second))
	if err != nil || refused.Granted || refused.Lease == nil || refused.Lease.ClientID != "tab-a" {
		t.Fatalf("conflict=%+v err=%v", refused, err)
	}
	terminal.Takeover = true
	taken, err := table.acquire(terminal, now.Add(12*time.Second))
	if err != nil || !taken.Granted || taken.Previous == nil || taken.Previous.ClientID != "tab-a" || taken.Lease.ExpiresAt != nil {
		t.Fatalf("takeover=%+v err=%v", taken, err)
	}
	if released, _ := table.release(web); released.Granted {
		t.Fatal("a displaced client released the new holder's lease")
	}
	if released, _ := table.release(terminal); !released.Granted || table.get(importTestID, now) != nil {
		t.Fatalf("release=%+v", released)
	}
	if _, err := table.acquire(web, now); err != nil {
		t.Fatal(err)
	}
	if table.get(importTestID, now.Add(time.Minute)) != nil {
		t.Fatal("expired web lease is still live")
	}
	other := model.LeaseRequest{ItemID: importTestID, Holder: model.LeaseHolderWeb, ClientID: "tab-b", TTLSeconds: 30}
	if afterExpiry, _ := table.acquire(other, now.Add(time.Minute)); !afterExpiry.Granted || afterExpiry.Previous != nil {
		t.Fatalf("expired lease was not vacant: %+v", afterExpiry)
	}
	for _, invalid := range []model.LeaseRequest{
		{ItemID: "bad", Holder: model.LeaseHolderWeb, ClientID: "x"},
		{ItemID: importTestID, Holder: "agent", ClientID: "x"},
		{ItemID: importTestID, Holder: model.LeaseHolderWeb},
		{ItemID: importTestID, Holder: model.LeaseHolderWeb, ClientID: "x", TTLSeconds: 3600},
	} {
		if _, err := table.acquire(invalid, now); err == nil {
			t.Fatalf("invalid lease request accepted: %+v", invalid)
		}
	}
}
