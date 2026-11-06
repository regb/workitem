package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/testutil"
	web "github.com/regb/workitem/internal/web"
)

func TestWebMediaReadsAreGeneratedAPICalls(t *testing.T) {
	f := newClientFixture(t)
	b := webBackend{f.cfg}
	item := f.create(t, "Browser item")
	ctx := context.Background()
	if err := b.CheckItem(ctx, item.Id); err != nil {
		t.Fatal(err)
	}
	if err := b.CheckItem(ctx, "01KZYHGDCECSFS4BJ2SNTQP49W"); err != web.ErrNotFound {
		t.Fatalf("exact ID check: %v", err)
	}
	// Attachments are served at a URL for the browser, from the API.
	if _, err := f.cfg.API.Items.Attach(ctx, connect.NewRequest(&v1.AttachRequest{ItemId: item.Id, Name: "evidence.txt", Content: []byte("evidence")})); err != nil {
		t.Fatal(err)
	}
	notes, err := f.cfg.API.Items.ListNotes(ctx, connect.NewRequest(&v1.ListNotesRequest{ItemId: item.Id}))
	if err != nil || len(notes.Msg.Attachments) != 1 {
		t.Fatalf("notes: %v %v", notes, err)
	}
	reader, meta, err := b.Attachment(ctx, item.Id, notes.Msg.Attachments[0].Attachment.Sha256)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if meta.Name != "evidence.txt" {
		t.Fatal(meta)
	}
}
func TestWebSocketIsPrivateAndNeverReplacesOtherFiles(t *testing.T) {
	root := testutil.ShortTempDir(t)
	path := filepath.Join(root, "web.sock")
	listener, err := listenWebSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if _, err := listenWebSocket(path); err == nil {
		t.Fatal("replaced live socket")
	}
	listener.Close()
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenWebSocket(path); err == nil {
		t.Fatal("replaced regular file")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "retain" {
		t.Fatal(data)
	}
}
