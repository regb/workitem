package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryPagesNewestFirstWithoutGaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	parent := ""
	for i := 0; i < 451; i++ {
		id := fmt.Sprintf("e%d", i)
		if err := encoder.Encode(map[string]any{"type": "message", "id": id, "parentId": parent, "message": map[string]any{"role": "user", "content": fmt.Sprintf("message %d", i)}}); err != nil {
			t.Fatal(err)
		}
		parent = id
	}
	f.Close()
	page, err := ReadHistory(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 200 || page.Messages[0].ID != "e251" || page.Messages[199].ID != "e450" || !page.HasMore || page.BeforeID != "e251" {
		t.Fatalf("latest page: %+v", page)
	}
	older, err := ReadHistoryPage(context.Background(), path, page.LeafID, page.BeforeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Messages) != 200 || older.Messages[0].ID != "e51" || older.Messages[199].ID != "e250" || older.BeforeID != "e51" {
		t.Fatalf("older page: %+v", older)
	}
	oldest, err := ReadHistoryPage(context.Background(), path, page.LeafID, older.BeforeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldest.Messages) != 51 || oldest.Messages[0].ID != "e0" || oldest.Messages[50].ID != "e50" || oldest.HasMore || oldest.BeforeID != "" {
		t.Fatalf("oldest page: %+v", oldest)
	}
	if _, err := ReadHistoryPage(context.Background(), path, "e450", "foreign-branch"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestHistorySupportsFilesBeyondOldSizeLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("large native-file regression")
	}
	path := filepath.Join(t.TempDir(), "history.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	parent := ""
	padding := strings.Repeat("x", 1<<20)
	for i := 0; i < 65; i++ {
		id := fmt.Sprintf("e%d", i)
		if err := encoder.Encode(map[string]any{"type": "message", "id": id, "parentId": parent, "message": map[string]any{"role": "user", "content": fmt.Sprintf("message %d ", i) + padding}}); err != nil {
			t.Fatal(err)
		}
		parent = id
	}
	f.Close()
	info, _ := os.Stat(path)
	if info.Size() <= 64<<20 {
		t.Fatal("fixture not large enough")
	}
	page, err := ReadHistory(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.LeafID != "e64" || len(page.Messages) == 0 || page.Messages[len(page.Messages)-1].ID != "e64" || !page.HasMore {
		t.Fatalf("large history: %+v", page)
	}
}
