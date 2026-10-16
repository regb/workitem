package cli

import (
	"context"
	"encoding/json"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"strings"
	"testing"
	"time"
)

func assertAppBlockerCommands(t *testing.T, a *app.App, client *coordinator.Client, ids []string) {
	t.Helper()
	ctx := context.Background()
	opts := func(id string) app.ResolveOptions { return app.ResolveOptions{Selector: id} }
	if r, err := a.AddBlockers(ctx, opts(ids[1]), []model.Blocker{{Kind: model.BlockerItem, ItemID: ids[0]}}); err != nil || len(r.Added) != 1 {
		t.Fatal(r, err)
	}
	before, err := client.CanonicalManifest(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, blocks := range [][]model.Blocker{
		{{Kind: model.BlockerItem, ItemID: ids[1]}},
		{{Kind: model.BlockerExternal, Event: "must roll back"}, {Kind: model.BlockerItem, ItemID: "01KZYHGDCECSFS4BJ2SNTQP49Z"}},
	} {
		if _, err := a.AddBlockers(ctx, opts(ids[0]), blocks); err == nil {
			t.Fatal("App bypassed dependency validation")
		}
		after, err := client.CanonicalManifest(ctx, ids[0])
		if err != nil || after.Revision != before.Revision || len(after.Manifest.Blockers) != len(before.Manifest.Blockers) {
			t.Fatal("rejected mutation partially committed", err)
		}
	}
	forged := before.Manifest
	forged.Blockers = nil
	payload, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ExecuteStoreCommand(ctx, coordinator.StoreCommandRequest{Command: coordinator.StoreCommand{ID: "raw-blocker-bypass", ProtocolVersion: coordinator.ProtocolVersion, Operation: coordinator.StoreManifestSave, ItemID: ids[0], ExpectedRevision: &before.Revision, PayloadDigest: coordinator.StorePayloadDigest(payload), CreatedAt: time.Now()}, Payload: payload})
	if err == nil || !strings.Contains(err.Error(), "typed manifest command") {
		t.Fatal("raw save bypassed blocker command", err)
	}
	if _, err := a.SetWorkItemState(ctx, opts(ids[1]), model.StateClosed, false); err != nil {
		t.Fatal(err)
	}
	closed, err := client.CanonicalManifest(ctx, ids[1])
	if err != nil || len(closed.Manifest.Blockers) != 0 || closed.Manifest.State != model.StateClosed {
		t.Fatal("App close did not commit blocker clearance", err)
	}
	events, err := client.ItemEvents(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		found = found || e.Type == "blocker.cleared" && e.Data["cause"] == "closed"
	}
	if !found {
		t.Fatal("close lost blocker clearance history")
	}
	if _, err := a.AddBlockers(ctx, opts(ids[2]), []model.Blocker{{Kind: model.BlockerItem, ItemID: ids[1]}}); err == nil {
		t.Fatal("App accepted a closed prerequisite")
	}
}
