package pi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConversationSnapshotRoundTrip(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.jsonl")
	content := []byte("{\"type\":\"session\",\"id\":\"test\"}\n{partial\n")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	exported, err := exportConversation(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "nested", "target.jsonl")
	if err := importConversation(target, exported); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("content=%q", got)
	}
}
