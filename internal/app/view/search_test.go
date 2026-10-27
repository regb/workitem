package view

import (
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestSearchMatchesEveryTermAcrossFieldsAndRanksByField(t *testing.T) {
	now := time.Now()
	item := func(id, title, state string, labels []string, age time.Duration) model.Manifest {
		return model.Manifest{ID: id, Slug: id, Title: title, State: state, Labels: labels, UpdatedAt: now.Add(-age)}
	}
	store := manifestStore{items: []model.Manifest{
		item("described", "Fix login", model.StateClosed, nil, time.Hour),
		item("titled", "Cache invalidation bug", model.StateActive, nil, 2*time.Hour),
		item("labelled", "Speed up builds", model.StateBacklog, []string{"cache"}, 3*time.Hour),
		item("noted", "Investigate flaky test", model.StateClosed, nil, time.Minute),
		item("unrelated", "Write docs", model.StateClosed, nil, 0),
	}}
	text := map[string]SearchText{
		"described": {Description: "Users stay logged in because the session cache\nkeeps stale tokens."},
		"noted":     {Notes: []string{"Root cause: a shared Cache between tests."}},
	}
	read := 0
	service := New(store)
	search := func(opts SearchOptions) SearchResult {
		return service.Search(opts, 2, func(m model.Manifest) SearchText { read++; return text[m.ID] })
	}
	result := search(SearchOptions{Query: "CACHE"})
	ids := []string{}
	for _, hit := range result.Items {
		ids = append(ids, hit.ID+":"+hit.Field)
	}
	if strings.Join(ids, " ") != "titled:title labelled:labels described:description noted:notes" || result.Total != 4 {
		t.Fatalf("hits = %v", ids)
	}
	if excerpt := result.Items[2].Excerpt; excerpt != "Users stay logged in because the session cache keeps stale tokens." {
		t.Fatalf("excerpt = %q", excerpt)
	}
	// Title and labels decide without reading description and notes.
	read = 0
	search(SearchOptions{Query: "cache"})
	if read != 3 {
		t.Fatalf("read text of %d items, want the 3 whose title and labels do not match", read)
	}
	if got := search(SearchOptions{Query: `"session cache keeps"`, State: model.StateClosed}); len(got.Items) != 1 || got.Items[0].ID != "described" {
		t.Fatalf("phrase across lines: %+v", got.Items)
	}
	if got := search(SearchOptions{Query: "cache login"}); len(got.Items) != 1 || got.Items[0].Field != SearchFieldTitle {
		t.Fatalf("terms in different fields: %+v", got.Items)
	}
	if got := search(SearchOptions{Query: "cache", State: model.StateClosed, Limit: 1}); len(got.Items) != 1 || got.Total != 2 || got.Items[0].ID != "described" {
		t.Fatalf("state and limit: %+v total %d", got.Items, got.Total)
	}
	if got := search(SearchOptions{Query: "  "}); len(got.Items) != 0 {
		t.Fatal("empty query matched")
	}
}

func TestExcerptCutsOnWordsAroundTheMatch(t *testing.T) {
	text := strings.Repeat("alpha beta gamma ", 20) + "needle " + strings.Repeat("delta epsilon ", 20)
	got := excerpt(text, []string{"needle"})
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || !strings.Contains(got, "needle") || len([]rune(got)) > excerptRunes+2 {
		t.Fatalf("excerpt = %q", got)
	}
	for _, word := range strings.Fields(strings.Trim(got, "…")) {
		if !strings.Contains("alpha beta gamma needle delta epsilon", word) {
			t.Fatalf("cut inside a word: %q", got)
		}
	}
}
