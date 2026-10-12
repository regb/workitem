package model

import "testing"

func TestIDDerivedSlugUsesTrailingIdentity(t *testing.T) {
	id := "01M25KWJPX294GB4MV933SGA1R"
	if got := IDDerivedSlug("  " + id + "  "); got != "item-933sga1r" {
		t.Fatalf("slug=%q", got)
	}
	if got := ItemBranchName("topic", id); got != "wi/topic-933sga1r" {
		t.Fatalf("branch=%q", got)
	}
}
