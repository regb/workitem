package coordinator

import (
	"context"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTitleCommandOnlyChangesMetadataAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := model.NewManifest(importTestID, "conversation", "New conversation", nil, false, time.Now(), model.Repository{RootAtCreation: "/repo", CreatedFromCommit: "original"}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "original-branch"))
	if err := native.CreateItem(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := seedManifests(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}
	before, _ := db.CanonicalManifest(m.ID)
	cmd := ManifestCommand{ID: "generated-title", ProtocolVersion: ProtocolVersion, Type: CommandTitleSet, ItemID: m.ID, ExpectedRevision: &before.Revision, Title: "Fix authentication tests", Actor: model.OperatorUser, CreatedAt: time.Now().UTC()}
	result, err := db.ExecuteManifestCommand(cmd)
	if err != nil || !result.Changed || result.Manifest.Title != cmd.Title || len(result.Events) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Manifest.Slug != m.Slug || !reflect.DeepEqual(result.Manifest.Checkout, m.Checkout) || !reflect.DeepEqual(result.Manifest.Repository, m.Repository) || result.Manifest.State != m.State {
		t.Fatal("title changed identity, lifecycle, or checkout")
	}
	repeated, err := db.ExecuteManifestCommand(cmd)
	if err != nil || !repeated.Duplicate || repeated.Revision != result.Revision {
		t.Fatal("title replay not idempotent")
	}
	for i, title := range []string{"", "line\nbreak", "escape\x1b", strings.Repeat("x", 513)} {
		revision := result.Revision
		invalid := cmd
		invalid.ID = string(rune('a' + i))
		invalid.Title = title
		invalid.ExpectedRevision = &revision
		if _, err := db.ExecuteManifestCommand(invalid); err == nil {
			t.Fatalf("invalid title accepted: %q", title)
		}
	}
	after, _ := db.CanonicalManifest(m.ID)
	if after.Manifest.Title != cmd.Title || after.Revision != result.Revision {
		t.Fatal("rejected command changed title")
	}
	stale := cmd
	stale.ID = "stale-title"
	stale.Title = "Should not overwrite"
	if _, err := db.ExecuteManifestCommand(stale); err == nil {
		t.Fatal("stale revision overwrote title")
	}
}
