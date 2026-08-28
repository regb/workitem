package porcelain

import "testing"

func TestSelectNextIsPureRingNavigation(t *testing.T) {
	ordered := []string{"A", "B", "C"}
	tests := []struct {
		name, current    string
		index            int
		inQueue, wrapped bool
	}{{"successor", "B", 2, true, false}, {"wrap", "C", 0, true, true}, {"outside", "busy", 0, false, false}, {"unresolved", "", 0, false, false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectNext(ordered, tt.current)
			if err != nil {
				t.Fatal(err)
			}
			if got.Index != tt.index || got.CurrentInQueue != tt.inQueue || got.Wrapped != tt.wrapped {
				t.Fatalf("selection=%+v", got)
			}
		})
	}
	if _, err := SelectNext(nil, ""); err == nil {
		t.Fatal("empty attention ring should fail")
	}
}
