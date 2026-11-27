package model_test

import (
	"testing"

	"github.com/regb/workitem/internal/model"
)

func TestParseAccessLevel(t *testing.T) {
	for name, want := range map[string]model.AccessLevel{"view": model.AccessView, " Control ": model.AccessControl, "MANAGE": model.AccessManage} {
		if got, err := model.ParseAccessLevel(name); err != nil || got != want {
			t.Errorf("ParseAccessLevel(%q) = %v, %v; want %v", name, got, err, want)
		}
	}
	for _, name := range []string{"none", "", "admin"} {
		if _, err := model.ParseAccessLevel(name); err == nil {
			t.Errorf("ParseAccessLevel(%q) accepted", name)
		}
	}
}

func TestItemAccess(t *testing.T) {
	const (
		root     = "01M25KWJPX294GB4MV933SGA1A"
		child    = "01M25KWJPX294GB4MV933SGA1B"
		grand    = "01M25KWJPX294GB4MV933SGA1C"
		stranger = "01M25KWJPX294GB4MV933SGA1D"
	)
	items := map[string]model.Manifest{
		root:  {ID: root, Owner: "user:op"},
		child: {ID: child, Owner: model.ItemPrincipal(root)},
		grand: {ID: grand, Owner: model.ItemPrincipal(child)},
	}
	lookup := func(id string) (model.Manifest, bool) { m, ok := items[id]; return m, ok }
	grants := []model.AccessEntry{
		{Subject: model.ItemPrincipal(stranger), ItemID: root, Level: model.AccessView},
		{Subject: model.ItemPrincipal(stranger), ItemID: root, Level: model.AccessControl},
		{Subject: model.EveryItemPrincipal, ItemID: child, Level: model.AccessMessage},
		{Subject: model.ItemPrincipal(child), ItemID: model.EveryItem, Level: model.AccessIntegrate},
	}
	for _, tc := range []struct {
		name    string
		subject string
		target  string
		entries []model.AccessEntry
		lookup  func(string) (model.Manifest, bool)
		want    model.AccessLevel
	}{
		{"itself", model.ItemPrincipal(root), root, nil, lookup, model.AccessManage},
		{"direct delegate", model.ItemPrincipal(root), child, nil, nil, model.AccessManage},
		{"transitive delegate", model.ItemPrincipal(root), grand, nil, lookup, model.AccessManage},
		{"transitive without lookup", model.ItemPrincipal(root), grand, nil, nil, model.AccessNone},
		{"delegate on its owner", model.ItemPrincipal(child), root, nil, lookup, model.AccessNone},
		{"highest matching grant", model.ItemPrincipal(stranger), root, grants, lookup, model.AccessControl},
		{"grant to every item", model.ItemPrincipal(stranger), child, grants, lookup, model.AccessMessage},
		{"grant on every item", model.ItemPrincipal(child), root, grants, lookup, model.AccessIntegrate},
		// Reading an item covers what it delegated, however far down, and
		// a higher grant on it still gives only reading there.
		{"read through the owner chain", model.ItemPrincipal(stranger), grand, grants, lookup, model.AccessView},
		{"read needs the owner chain", model.ItemPrincipal(stranger), grand, grants, nil, model.AccessNone},
		{"a grant on the item itself stands", model.ItemPrincipal(stranger), child, grants, lookup, model.AccessMessage},
		{"no grant", model.ItemPrincipal(stranger), grand, nil, lookup, model.AccessNone},
	} {
		if got := model.ItemAccess(tc.subject, items[tc.target], tc.entries, tc.lookup); got != tc.want {
			t.Errorf("%s: ItemAccess = %v, want %v", tc.name, got, tc.want)
		}
	}
}
