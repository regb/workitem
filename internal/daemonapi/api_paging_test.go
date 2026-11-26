package daemonapi_test

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

// The lists that grow with an item's life come in pages: a page_size, at
// most 500, and a token for the rest. What rides along, such as an item's
// current attachments, is on every page.
func TestPagingNotes(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	item := newItem(t, f, f.Repository(t, "test"), "Paged")
	for i := range 5 {
		call(t, f.Clients.Items.AddNote, &v1.AddNoteRequest{ItemId: item.Id, Text: fmt.Sprintf("note %d", i)})
	}
	call(t, f.Clients.Items.Attach, &v1.AttachRequest{ItemId: item.Id, Name: "REPORT.md", Content: []byte("# Report\n")})

	all := call(t, f.Clients.Items.ListNotes, &v1.ListNotesRequest{ItemId: item.Id})
	if len(all.Notes) != 6 || all.NextPageToken != "" {
		t.Fatalf("one page of everything: %d notes, token %q", len(all.Notes), all.NextPageToken)
	}
	var texts []string
	token := ""
	for pages := 0; ; pages++ {
		page := call(t, f.Clients.Items.ListNotes, &v1.ListNotesRequest{ItemId: item.Id, PageSize: 2, PageToken: token})
		if len(page.Notes) > 2 || len(page.Attachments) != 1 {
			t.Fatalf("page %d: %d notes, %d attachments", pages, len(page.Notes), len(page.Attachments))
		}
		for _, note := range page.Notes {
			texts = append(texts, note.Text)
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	for i, note := range all.Notes {
		if texts[i] != note.Text {
			t.Fatalf("pages read %q, want %q", texts, all.Notes)
		}
	}
	refused(t, connect.CodeInvalidArgument, f.Clients.Items.ListNotes, &v1.ListNotesRequest{ItemId: item.Id, PageToken: "not-a-token"})
}
