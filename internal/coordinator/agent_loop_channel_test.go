package coordinator

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/runtimepath"
	"github.com/regb/workitem/internal/testutil"
)

func TestActiveLoopRequiresReachableBridgeNotJustLivePID(t *testing.T) {
	for _, mode := range []string{"tui", "headless"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			db, err := OpenDatabase(root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Now().UTC()
			old := now.Add(-12 * time.Hour)
			id := importTestID
			runtime := model.AgentRuntime{ID: "owner", WorkItemID: id, State: model.AgentRuntimeRunning, Mode: mode, HostPID: os.Getpid(), Capabilities: model.RuntimeCapabilities{Prompt: true, Cancel: true}}
			activity := RuntimeActivity{WorkItemID: id, RuntimeID: runtime.ID, Source: "daemon.runtime.live", AgentState: "active", TurnState: "busy", LastEventAt: &old, LastRequestedAt: &old}
			updates := []ProjectionUpdate{}
			for _, record := range []struct {
				name  string
				value any
			}{{RuntimeOwnershipProjection, runtime}, {RuntimeActivityProjection, activity}} {
				data, _ := json.Marshal(record.value)
				updates = append(updates, ProjectionUpdate{Projection: record.name, Key: id, Value: data})
			}
			if err := db.UpdateProjections(updates); err != nil {
				t.Fatal(err)
			}
			observer := NewExternalObserver(db, root)
			observer.runtimeSocketRoot = testutil.ShortTempDir(t)
			path := runtimepath.SocketPath(observer.runtimeSocketRoot, "", id, runtime.ID, false)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			observation := AgentObservation{Runtime: &runtime, ProcessOnline: true, Process: ProjectedProcess{RuntimeHostPID: os.Getpid()}}
			if err := observer.applyActionability(id, &observation); err != nil {
				t.Fatal(err)
			}
			if observation.Status != "busy" || observation.AgentLoopState != "active" {
				t.Fatalf("elapsed time must not settle an observed active loop: %+v", observation)
			}
			// A later native terminal assistant message does not prove that
			// queued work/retries ended, especially once the bridge is lost.
			index, _ := json.Marshal(ConversationIndex{WorkItemID: id, InferredTurnState: "idle", LastTurnActivity: &ConversationEventFact{Timestamp: now, Role: "assistant", Terminal: true}})
			if err := db.UpdateProjections([]ProjectionUpdate{{Projection: ConversationProjection, Key: id, Value: index}}); err != nil {
				t.Fatal(err)
			}
			// Leave an orphan socket inode: existence alone does not prove that a
			// native bridge is still serving, even though its host PID is alive.
			listener.SetUnlinkOnClose(false)
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			if err := observer.applyActionability(id, &observation); err != nil {
				t.Fatal(err)
			}
			if observation.Status != "problem" || observation.AgentLoopState == "active" || !observation.ProcessOnline {
				t.Fatalf("dead bridge retained authoritative busy state: %+v", observation)
			}
			if !observation.LastActivityAt.Equal(now) {
				t.Fatal("inspection rewrote activity history")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := observer.applyActionability(id, &observation); err != nil {
				t.Fatal(err)
			}
			if observation.Status != "problem" {
				t.Fatal("missing socket became proof of settlement")
			}
			// Recovery is observed, not a destructive rewrite of retained events.
			recovered, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			if err := observer.applyActionability(id, &observation); err != nil {
				t.Fatal(err)
			}
			if observation.Status != "busy" || observation.AgentLoopState != "active" {
				t.Fatal("recovered bridge did not restore active observation")
			}
			var retained RuntimeActivity
			if _, err := db.ReadProjection(RuntimeActivityProjection, id, &retained); err != nil {
				t.Fatal(err)
			}
			if retained.AgentState != "active" || !retained.LastEventAt.Equal(old) {
				t.Fatal("health inspection rewrote retained runtime facts")
			}
		})
	}
}
