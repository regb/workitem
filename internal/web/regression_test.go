package web

import (
	"context"
	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"net/http"
	"testing"
)

func TestMalformedAccessURLReturnsError(t *testing.T) {
	for _, origin := range []string{"https://%", "https://[", "https://user:password@example.test", "https://example.test/path"} {
		if _, err := New(&fakeBackend{}, Options{AccessURL: origin}); err == nil {
			t.Fatalf("invalid origin accepted: %s", origin)
		}
	}
}

type pagingBackend struct {
	fakeBackend
	id, leaf, before string
}

func (b *pagingBackend) History(_ context.Context, id, leaf, before string) (driverprotocol.History, error) {
	b.id, b.leaf, b.before = id, leaf, before
	return driverprotocol.History{Messages: []driverprotocol.HistoryMessage{{ID: "older", Role: "user", Text: "old"}}, LeafID: leaf}, nil
}
func TestHistoryPagingUsesItemAndBranchNotNativePath(t *testing.T) {
	b := &pagingBackend{}
	s, err := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := request(s, http.MethodGet, "/api/items/"+itemID+"/history?leaf=current&before=previous", "")
	if w.Code != 200 || b.id != itemID || b.leaf != "current" || b.before != "previous" {
		t.Fatalf("history page: %d %s %+v", w.Code, w.Body.String(), b)
	}
}
