package driverprotocol

import "testing"

func TestContentSnapshotOwnsToolState(t *testing.T) {
	s := &ContentServer{}
	s.UpdateTool(EventToolStarted, "", "call", "bash", "pwd", "", false)
	snapshot := s.snapshot()
	s.UpdateTool(EventToolCompleted, "", "call", "bash", "", "/repo", false)
	if snapshot.Tool.Status != "running" || snapshot.Tool.Output != "" {
		t.Fatalf("snapshot changed after update: %+v", snapshot.Tool)
	}
	if value := s.snapshot(); value.Tool.Status != "completed" || value.Tool.Output != "/repo" {
		t.Fatalf("new snapshot: %+v", value.Tool)
	}
}
