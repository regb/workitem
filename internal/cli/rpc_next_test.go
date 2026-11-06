package cli

import (
	"strings"
	"testing"
)

func TestNextInRingNeverReturnsTheCurrentItem(t *testing.T) {
	for _, test := range []struct {
		ring             []string
		current, want    string
		wrapped, inQueue bool
	}{
		{[]string{"a", "b", "c"}, "a", "b", false, true},
		{[]string{"a", "b", "c"}, "c", "a", true, true},
		{[]string{"a", "b", "c"}, "x", "a", false, false},
		{[]string{"a", "b", "c"}, "", "a", false, false},
		{[]string{"a"}, "a", "", false, true},
		{nil, "a", "", false, false},
	} {
		got, wrapped, inQueue := nextInRing(test.ring, test.current)
		if got != test.want || wrapped != test.wrapped || inQueue != test.inQueue {
			t.Errorf("nextInRing(%v, %q) = %q, %v, %v; want %q, %v, %v", test.ring, test.current, got, wrapped, inQueue, test.want, test.wrapped, test.inQueue)
		}
	}
}

func TestNextOptionsAcceptOneActionAndItsConditions(t *testing.T) {
	for _, test := range []struct {
		options nextOptions
		problem string
	}{
		{nextOptions{}, ""},
		{nextOptions{deferCurrent: true}, ""},
		{nextOptions{waitCurrent: true, until: "1h"}, ""},
		{nextOptions{waitCurrent: true, on: "-"}, ""},
		{nextOptions{closeCurrent: true, resolution: "abandoned"}, ""},
		{nextOptions{deferCurrent: true, closeCurrent: true}, "only one of"},
		{nextOptions{until: "1h"}, "require --wait"},
		{nextOptions{waitCurrent: true}, "requires one of"},
		{nextOptions{waitCurrent: true, until: "1h", event: "review"}, "requires one of"},
		{nextOptions{resolution: "completed"}, "requires --close"},
		{nextOptions{closeCurrent: true, resolution: "done"}, "invalid resolution"},
	} {
		err := test.options.validate()
		if test.problem == "" && err != nil || test.problem != "" && (err == nil || !strings.Contains(err.Error(), test.problem)) {
			t.Errorf("%+v: got %v, want %q", test.options, err, test.problem)
		}
	}
}
