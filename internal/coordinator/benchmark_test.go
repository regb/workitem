package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func BenchmarkManifestProjectionRead100(b *testing.B) {
	db, err := OpenDatabase(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	values := make(map[string]ImportedManifest, 100)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("01KZYHGDCECSFS4BJ2SNTQ%04d", i)
		manifest := model.NewManifest(id, fmt.Sprintf("item-%d", i), fmt.Sprintf("Projected item %d", i), []string{"benchmark"}, i%7 == 0, time.Unix(int64(i), 0).UTC(), model.Repository{RemoteURL: "https://example.invalid/repository.git"}, model.Checkout{})
		digest := sha256.Sum256([]byte(id))
		values[id] = ImportedManifest{Manifest: manifest, Digest: hex.EncodeToString(digest[:])}
	}
	if _, err := db.SyncImportedManifests(values, true); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manifests, err := db.ListManifests()
		if err != nil || len(manifests) != 100 {
			b.Fatalf("manifests=%d err=%v", len(manifests), err)
		}
	}
}

func BenchmarkWaitAndActionabilityQueue100(b *testing.B) {
	db, err := OpenDatabase(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	values := make(map[string]ImportedManifest, 100)
	updates := make([]ProjectionUpdate, 0, 100)
	now := time.Now().UTC()
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("01KZYHGDCECSFS4BJ2SNTQ%04d", i)
		manifest := model.NewManifest(id, fmt.Sprintf("queue-%d", i), fmt.Sprintf("Queue item %d", i), nil, i%7 == 0, now.Add(time.Duration(i)*time.Second), model.Repository{}, model.Checkout{})
		manifest.State = model.StateWorking
		digest := sha256.Sum256([]byte(id))
		values[id] = ImportedManifest{Manifest: manifest, Digest: hex.EncodeToString(digest[:])}
		requestedAt := now.Add(time.Duration(i) * time.Minute)
		observation, _ := json.Marshal(AgentObservation{WorkItemID: id, Status: "idle", Activity: AttentionActivity{WorkItemID: id, LastRequestedAt: &requestedAt}, ObservedAt: now})
		updates = append(updates, ProjectionUpdate{Projection: AgentObservationProjection, Key: id, Value: observation})
	}
	if _, err := db.SyncImportedManifests(values, true); err != nil {
		b.Fatal(err)
	}
	if err := db.UpdateProjections(updates); err != nil {
		b.Fatal(err)
	}
	itemID := "01KZYHGDCECSFS4BJ2SNTQ0000"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		canonical, err := db.CanonicalManifest(itemID)
		if err != nil {
			b.Fatal(err)
		}
		command := ManifestCommand{ID: fmt.Sprintf("bench-wait-%d", i), ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: itemID, ExpectedRevision: &canonical.Revision, Actor: "user", TargetState: model.StateWaiting, CreatedAt: now.Add(time.Duration(i) * time.Millisecond)}
		result, err := db.ExecuteManifestCommandWithQueue(command, ActionabilityQueueOptions{})
		if err != nil || len(result.Queue.Candidates) != 99 {
			b.Fatalf("candidates=%d err=%v", len(result.Queue.Candidates), err)
		}
		b.StopTimer()
		waitingRevision := result.Command.Revision
		_, err = db.ExecuteManifestCommand(ManifestCommand{ID: fmt.Sprintf("bench-working-%d", i), ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: itemID, ExpectedRevision: &waitingRevision, Actor: "user", TargetState: model.StateWorking, CreatedAt: now.Add(time.Duration(i)*time.Millisecond + time.Microsecond)})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}
