package daemonapi

import "testing"

func TestAPageIsBounded(t *testing.T) {
	for _, c := range []struct {
		name         string
		size         uint32
		total, limit int
		start, end   int
		more         bool
	}{
		{"unbounded list, no size", 0, 900, 0, 0, 900, false},
		{"bounded list, no size", 0, 900, maxPage, 0, maxPage, true},
		{"bounded list, size over the bound", 800, 900, maxPage, 0, maxPage, true},
		{"size within the bound", 10, 25, maxPage, 0, 10, true},
		{"last page", 10, 10, maxPage, 0, 10, false},
		{"empty list", 10, 0, maxPage, 0, 0, false},
	} {
		start, end, next, err := page("", c.size, c.total, c.limit)
		if err != nil || start != c.start || end != c.end || (next != "") != c.more {
			t.Errorf("%s: %d..%d next %q err %v", c.name, start, end, next, err)
			continue
		}
		if next != "" {
			if again, _, _, err := page(next, c.size, c.total, c.limit); err != nil || again != end {
				t.Errorf("%s: next page starts at %d, %v", c.name, again, err)
			}
		}
	}
	if _, _, _, err := page("bm90LWEtbnVtYmVy", 10, 5, maxPage); err == nil {
		t.Error("accepted a token that is not an offset")
	}
	if _, _, _, err := page("OQ", 10, 5, maxPage); err == nil {
		t.Error("accepted an offset past the end")
	}
}
