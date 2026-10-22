package driverprotocol

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContentSnapshotsAreBoundedReadOnlyAndOwnerScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	server, err := ListenContent(path, "runtime", "item", true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions %v %v", info, err)
	}
	if _, err := ListenContent(path, "other", "item", true); err == nil {
		t.Fatal("rebound live content socket")
	}
	server.Update("message.started", "", "")
	server.Update(EventMessageDelta, "hello", "")
	server.Update(EventMessageDelta, " 🌍", "")
	for i := 0; i < 2; i++ {
		value, err := ReadContent(context.Background(), path, "runtime", "item")
		if err != nil {
			t.Fatal(err)
		}
		if value.Text != "hello 🌍" {
			t.Fatalf("snapshot=%+v", value)
		}
	}
	if _, err := ReadContent(context.Background(), path, "superseded", "item"); err == nil {
		t.Fatal("accepted wrong runtime")
	}
	server.Update(EventMessageDelta, strings.Repeat("x", 300<<10), "")
	value, err := ReadContent(context.Background(), path, "runtime", "item")
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Text) > 256<<10 {
		t.Fatal("unbounded snapshot")
	}
	// A completed message keeps its text until the next one starts; readers
	// hide it once history has it (Content.SavedAs).
	server.Update(EventMessageCompleted, "private final", "")
	value, err = ReadContent(context.Background(), path, "runtime", "item")
	if err != nil || value.Activity != "Message saved" || !strings.HasPrefix(value.Text, "hello") {
		t.Fatalf("completed message: activity=%q text bytes=%d err=%v", value.Activity, len(value.Text), err)
	}
	server.UpdateTool(EventToolStarted, "", "tool-1", "bash", `{"command":"pwd"}`, "", false)
	value, err = ReadContent(context.Background(), path, "runtime", "item")
	if err != nil || value.Tool == nil || value.Tool.Status != "running" || !strings.Contains(value.Tool.Input, "pwd") {
		t.Fatalf("running tool snapshot %+v %v", value, err)
	}
	server.UpdateTool(EventToolCompleted, "", "tool-1", "bash", "", "/tmp", false)
	value, err = ReadContent(context.Background(), path, "runtime", "item")
	if err != nil || value.Tool == nil || value.Tool.Status != "completed" || value.Tool.Output != "/tmp" {
		t.Fatalf("completed tool snapshot %+v %v", value, err)
	}
}
