package item

import (
	"context"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/testutil"
)

func TestUntitledCreationUsesIDDerivedSlug(t *testing.T) {
	id := testutil.ID(t, 14)
	base := model.IDDerivedSlug(id)
	for _, tc := range []struct {
		name, explicit, want string
		existing             []model.Manifest
		wantError            bool
	}{
		{name: "generated", want: base},
		{name: "explicit override", explicit: "custom", want: "custom"},
		{name: "generated collision", existing: []model.Manifest{{Slug: base, State: model.StateBacklog}}, want: base + "-2"},
		{name: "archived slug reusable", existing: []model.Manifest{{Slug: base, State: model.StateClosed}}, want: base},
		{name: "explicit collision", explicit: "custom", existing: []model.Manifest{{Slug: "custom", State: model.StateBacklog}}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(itemStore{items: tc.existing}, creationGit{root: t.TempDir()}, nil, testutil.Time, func() (string, error) { return id, nil }, nil, nil)
			result, err := s.NewWorkItem(context.Background(), NewOptions{Untitled: true, Slug: tc.explicit, Description: "Details are separate", CWD: t.TempDir(), PrepareOnly: true})
			if tc.wantError {
				if err == nil {
					t.Fatal("explicit collision accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Manifest.Title != model.UntitledTitle || result.Manifest.Slug != tc.want || result.Manifest.Checkout.Specification.Branch != model.ItemBranchName(tc.want, id) || result.Description != "Details are separate" {
				t.Fatalf("result=%+v", result)
			}
			if result.Manifest.PrimaryConversation != nil || result.Manifest.Checkout.Claim != nil {
				t.Fatal("untitled creation allocated resources")
			}
		})
	}
}

func TestCreationRequiresTitleOrUntitledButNotBoth(t *testing.T) {
	s := New(itemStore{}, creationGit{}, nil, testutil.Time, func() (string, error) { t.Fatal("invalid naming allocated ID"); return "", nil }, nil, nil)
	for _, opts := range []NewOptions{{}, {Title: " "}, {Untitled: true, Title: "Named"}} {
		_, err := s.NewWorkItem(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "title") {
			t.Fatalf("opts=%+v err=%v", opts, err)
		}
	}
}
