package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func editFixture(t *testing.T) (*Server, model.Manifest) {
	t.Helper()
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := model.NewManifest(importTestID, "stable", "Original", []string{"old"}, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "stable-branch"))
	if err := native.CreateItem(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native.ItemDir(m.ID), model.DescriptionFilename), []byte("original description"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := seedManifests(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}
	return &Server{database: db, native: native}, m
}
func editRequest(t *testing.T, s *Server, id string, edit MetadataEdit, description *string) EditItemRequest {
	t.Helper()
	canonical, err := s.database.CanonicalManifest(importTestID)
	if err != nil {
		t.Fatal(err)
	}
	if description != nil {
		current, err := ReadEditDescription(s.native, importTestID)
		if err != nil {
			t.Fatal(err)
		}
		edit.DescriptionDigest = DescriptionDigest(*description)
		edit.PreviousDescriptionDigest = DescriptionDigest(current)
	}
	return EditItemRequest{Command: ManifestCommand{ID: id, ProtocolVersion: ProtocolVersion, Type: CommandEdit, ItemID: importTestID, ExpectedRevision: &canonical.Revision, Actor: "user", Edit: &edit, CreatedAt: time.Now().UTC()}, Description: description}
}

func TestEditItemCommitsCombinedPatchAndReplays(t *testing.T) {
	s, m := editFixture(t)
	title := "Clarified"
	deep := true
	desc := "new private description"
	request := editRequest(t, s, "combined", MetadataEdit{Title: &title, AddLabels: []string{"NEW"}, RemoveLabels: []string{"old"}, DeepWork: &deep}, &desc)
	result, err := s.editItem(request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Manifest.Title != title || !result.Manifest.DeepWork || !reflect.DeepEqual(result.Manifest.Labels, []string{"new"}) || result.Manifest.Slug != m.Slug || result.Manifest.Checkout.Specification.Branch != m.Checkout.Specification.Branch || len(result.Events) != 1 {
		t.Fatalf("result=%+v", result)
	}
	current, err := ReadEditDescription(s.native, m.ID)
	if err != nil || current != desc {
		t.Fatalf("description=%q err=%v", current, err)
	}
	replay, err := s.editItem(request)
	if err != nil || !replay.Duplicate || replay.Revision != result.Revision {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if pending, err := s.database.PendingNativeWrites(); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	// The command outcome, event, and pending intent contain hashes, not native text.
	stored, err := os.ReadFile(s.database.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), desc) {
		t.Fatal("description content entered database")
	}
}

func TestEditItemRejectsEntireInvalidPatch(t *testing.T) {
	for _, kind := range []string{"labels", "overlap", "title", "stale revision", "description conflict", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := editFixture(t)
			before, _ := s.database.CanonicalManifest(importTestID)
			title := "Valid title"
			deep := true
			desc := "not applied"
			edit := MetadataEdit{Title: &title, DeepWork: &deep, AddLabels: []string{"new"}}
			request := editRequest(t, s, "invalid", edit, &desc)
			switch kind {
			case "labels":
				request.Command.Edit.AddLabels = []string{"!invalid"}
			case "overlap":
				request.Command.Edit.RemoveLabels = []string{"NEW"}
			case "title":
				bad := "bad\ntitle"
				request.Command.Edit.Title = &bad
			case "stale revision":
				revision := before.Revision + 1
				request.Command.ExpectedRevision = &revision
			case "description conflict":
				os.WriteFile(filepath.Join(s.native.ItemDir(importTestID), model.DescriptionFilename), []byte("concurrent description"), 0600)
			case "symlink":
				path := filepath.Join(s.native.ItemDir(importTestID), model.DescriptionFilename)
				os.Remove(path)
				target := filepath.Join(t.TempDir(), "outside")
				os.WriteFile(target, []byte("outside"), 0600)
				os.Symlink(target, path)
			}
			if _, err := s.editItem(request); err == nil {
				t.Fatal("invalid edit accepted")
			}
			after, _ := s.database.CanonicalManifest(importTestID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid edit changed canonical metadata")
			}
			pending, err := s.database.PendingNativeWrites()
			if err != nil || len(pending) != 0 {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
		})
	}
}

func TestEditDescriptionInterruptedWriteRecoversWithoutOverwritingConcurrentChanges(t *testing.T) {
	s, _ := editFixture(t)
	title := "Committed title"
	desc := "staged content"
	request := editRequest(t, s, "interrupted", MetadataEdit{Title: &title}, &desc)
	if err := StageDescription(s.database, request.Command.ID, desc); err != nil {
		t.Fatal(err)
	}
	result, err := s.database.ExecuteManifestCommand(request.Command)
	if err != nil || !result.Changed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	path := filepath.Join(s.native.ItemDir(importTestID), model.DescriptionFilename)
	if err := os.WriteFile(path, []byte("concurrent"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeNativeWrites(context.Background(), s.database, s.native); err != nil {
		t.Fatal(err)
	}
	if err := CleanupOrphanedStages(s.database); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(descriptionStagePath(s.database, request.Command.ID))
	if err != nil || string(data) != desc {
		t.Fatal("conflicting stage was lost")
	}
	current, _ := os.ReadFile(path)
	if string(current) != "concurrent" {
		t.Fatal("concurrent native edit overwritten")
	}
	if _, err := s.editItem(editRequest(t, s, "blocked", MetadataEdit{}, nil)); err == nil {
		t.Fatal("pending edit was replaced")
	}
	// Operator reconciliation restores the expected baseline, after which replay
	// materializes the original committed edit, never a different payload.
	if err := os.WriteFile(path, []byte("original description"), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := s.editItem(request)
	if err != nil || !replay.Duplicate {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	current, _ = os.ReadFile(path)
	if string(current) != desc {
		t.Fatal("committed staged content not recovered")
	}
	// A crash after rename but before clearing the outbox is also idempotent.
	pending := pendingNativeWrite{CommandID: "already-renamed", Operation: "edit-description", Manifest: result.Manifest, DescriptionDigest: DescriptionDigest(desc), PreviousDescriptionDigest: DescriptionDigest("original description")}
	if err := materializeDescriptionEdit(s.database, s.native, pending); err != nil {
		t.Fatal(err)
	}
}

func TestEditReportsCommittedMetadataWhenNativeWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires non-root user")
	}
	s, _ := editFixture(t)
	title, description := "Retained title", "pending description"
	request := editRequest(t, s, "native-failure", MetadataEdit{Title: &title}, &description)
	dir := s.native.ItemDir(importTestID)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	result, err := s.editItem(request)
	if err == nil || !strings.Contains(err.Error(), "committed") || result.Manifest.Title != title {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	current, err := ReadEditDescription(s.native, importTestID)
	if err != nil || current != "original description" {
		t.Fatalf("description=%q err=%v", current, err)
	}
	canonical, _ := s.database.CanonicalManifest(importTestID)
	if canonical.Manifest.Title != title {
		t.Fatal("committed metadata was rolled back")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	replay, err := s.editItem(request)
	if err != nil || !replay.Duplicate {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	current, _ = ReadEditDescription(s.native, importTestID)
	if current != description {
		t.Fatal("pending description was not recovered")
	}
}

func TestEditDescriptionRecoversOnDaemonRestart(t *testing.T) {
	s, _ := editFixture(t)
	description := "recover after restart"
	request := editRequest(t, s, "restart", MetadataEdit{}, &description)
	if err := StageDescription(s.database, request.Command.ID, description); err != nil {
		t.Fatal(err)
	}
	if _, err := s.database.ExecuteManifestCommand(request.Command); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(s.database.path)
	if err := s.database.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewServer(root, filepath.Join(testutil.ShortTempDir(t), "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	current, err := ReadEditDescription(restarted.native, importTestID)
	if err != nil || current != description {
		t.Fatalf("description=%q err=%v", current, err)
	}
	pending, err := restarted.database.PendingNativeWrites()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	if _, err := os.Stat(descriptionStagePath(restarted.database, request.Command.ID)); !os.IsNotExist(err) {
		t.Fatal("completed stage retained")
	}
}

func TestEditRequestSupportsFullDescriptionLimit(t *testing.T) {
	desc := strings.Repeat("\x00", MaxDescriptionBytes)
	request := EditItemRequest{Description: &desc}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) >= MaxRequestBytes {
		t.Fatalf("envelope=%d err=%v", len(encoded), err)
	}
	if err := ValidateDescription(desc); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDescription(desc + "x"); err == nil {
		t.Fatal("oversized description accepted")
	}
}
