package daemonapi

import (
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

func TestAnOwnerCountsItsWorkingDelegations(t *testing.T) {
	busy, idle := v1.AgentStatus_AGENT_STATUS_BUSY, v1.AgentStatus_AGENT_STATUS_IDLE
	owner := "01K1OWNER0000000000000000"
	manifest := func(id, ownedBy, state string) model.Manifest {
		m := model.NewManifest(id, id, id, nil, false, time.Now(), model.Repository{}, model.Checkout{})
		m.Owner, m.State = ownedBy, state
		return m
	}
	of := model.ItemPrincipal(owner)
	manifests := []model.Manifest{
		manifest("01K1BUSY00000000000000000", of, model.StateActive),
		manifest("01K1BACKGROUND00000000000", of, model.StateActive),
		manifest("01K1ASKING000000000000000", of, model.StateActive),
		manifest("01K1IDLE00000000000000000", of, model.StateActive),
		manifest("01K1BACKLOG00000000000000", of, model.StateBacklog),
		manifest("01K1OTHER0000000000000000", model.ItemPrincipal("01K1ELSE00000000000000000"), model.StateActive),
		manifest("01K1MINE00000000000000000", model.OperatorUser, model.StateActive),
	}
	observations := map[string]*v1.ItemObservation{
		"01K1BUSY00000000000000000": {AgentStatus: busy},
		"01K1BACKGROUND00000000000": {AgentStatus: idle, BackgroundTasks: 1},
		"01K1ASKING000000000000000": {AgentStatus: busy, Asking: true},
		"01K1IDLE00000000000000000": {AgentStatus: idle, Unread: true},
		"01K1BACKLOG00000000000000": {AgentStatus: busy},
		"01K1OTHER0000000000000000": {AgentStatus: busy},
		"01K1MINE00000000000000000": {AgentStatus: busy},
	}
	if got := countWorking(manifests, owner, func(m model.Manifest) *v1.ItemObservation { return observations[m.ID] }); got != 2 {
		t.Fatalf("working delegations = %d, want 2", got)
	}
}
