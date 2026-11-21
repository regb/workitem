package driverprotocol

import "testing"

func TestContentSnapshotOwnsToolState(t *testing.T) {
	s := &ContentServer{}
	s.UpdateTool(EventToolStarted, "", "call", "bash", "pwd", "", false)
	snapshot := s.snapshot()
	s.UpdateTool(EventToolCompleted, "", "call", "bash", "", "/repo", false)
	if snapshot.LastTool().Status != "running" || snapshot.LastTool().Output != "" {
		t.Fatalf("snapshot changed after update: %+v", snapshot.LastTool())
	}
	if value := s.snapshot(); value.LastTool().Status != "completed" || value.LastTool().Output != "/repo" {
		t.Fatalf("new snapshot: %+v", value.LastTool())
	}
}
