package main

import (
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// TestAClosedItemIsStillNamedByItsSlug: wi show on a closed item's slug
// shows that item, not another one that mentions it.
func TestAClosedItemIsStillNamedByItsSlug(t *testing.T) {
	f := newBinaryFixture(t)
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	var first, second v1.CreateItemResponse
	f.json(&first, "new", "--repo", repo.Repository.Id, "--title", "Auth refactor")
	f.json(&second, "new", "--repo", repo.Repository.Id, "--title", "Auth refactor follow-up")
	slug := first.Item.Slug
	f.run("close", "--resolution", "completed", first.Item.Id)
	var shown v1.GetItemResponse
	f.json(&shown, "show", slug)
	if shown.Item.Id != first.Item.Id || shown.Item.State != v1.ItemState_ITEM_STATE_CLOSED {
		t.Fatalf("wi show %s = %s (%s), want the closed %s", slug, shown.Item.Id, shown.Item.Title, first.Item.Id)
	}
}
