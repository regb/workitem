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
	if got := ItemBranchName("Fix Race", "01K1ABCDE0000000000000000"); got != "wi/fix-race-00000000" {
		t.Fatalf("branch=%q", got)
	}
	if got := TerminalSessionName("01K1ABCDE0000000000000000"); got != "wi-01K1ABCDE000" {
		t.Fatalf("session=%q", got)
	}
}
