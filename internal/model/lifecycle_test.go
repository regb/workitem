package model

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeStateInputAcceptsOnlyCurrentNames(t *testing.T) {
	for input, want := range map[string]string{"backlog": StateBacklog, "active": StateActive, "closed": StateClosed, " Active ": StateActive} {
		if got, err := NormalizeStateInput(input); err != nil || got != want {
			t.Fatalf("%q = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"working", "waiting", "archived", "done", ""} {
		if _, err := NormalizeStateInput(input); err == nil {
			t.Fatalf("%q accepted", input)
		}
	}
}

func TestBlockerValidationAndOpenness(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	valid := []Blocker{{Kind: BlockerTime, Until: &future}, {Kind: BlockerItem, ItemID: "01KZYHGDCECSFS4BJ2SNTQP49W"}, {Kind: BlockerExternal, Event: "PR review"}}
	for _, blocker := range valid {
		if err := ValidateBlocker(blocker, "01KZYHGDCECSFS4BJ2SNTQP49V", now); err != nil {
			t.Fatalf("%+v: %v", blocker, err)
		}
	}
	invalid := []Blocker{{Kind: BlockerTime, Until: &past}, {Kind: BlockerTime}, {Kind: BlockerItem, ItemID: "01KZYHGDCECSFS4BJ2SNTQP49V"}, {Kind: BlockerExternal}, {Kind: BlockerExternal, Event: "two\nlines"}, {Kind: "snooze", Event: "x"}, {Kind: BlockerTime, Until: &future, Event: "release"}, {Kind: BlockerItem, ItemID: "01KZYHGDCECSFS4BJ2SNTQP49W", Event: "x"}, {Kind: BlockerExternal, Event: "x", Meta: map[string]string{"Bad Key": "x"}}, {Kind: BlockerExternal, Event: "x", Meta: map[string]string{"pr": "line\nbreak"}}}
	for _, blocker := range invalid {
		if err := ValidateBlocker(blocker, "01KZYHGDCECSFS4BJ2SNTQP49V", now); err == nil {
			t.Fatalf("%+v accepted", blocker)
		}
	}
	m := Manifest{Blockers: []Blocker{{ID: "a", Kind: BlockerTime, Until: &future}}}
	if !m.Waiting(now) || m.Waiting(future.Add(time.Second)) {
		t.Fatal("time blocker openness is wrong")
	}
}

func TestBlockerDescribeAndMeta(t *testing.T) {
	until := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		blocker Blocker
		want    string
	}{
		{Blocker{Kind: BlockerTime, Until: &until}, "until 2026-10-20 09:00"},
		{Blocker{Kind: BlockerItem, ItemID: "01KZ"}, "on 01KZ"},
		{Blocker{Kind: BlockerExternal, Event: "PR review"}, "PR review"},
	} {
		if got := tc.blocker.Describe(); got != tc.want {
			t.Fatalf("describe = %q, want %q", got, tc.want)
		}
	}
	meta, err := ParseBlockerMeta([]string{"source=github", "pr=https://example.com/1?a=b"})
	if err != nil || meta["pr"] != "https://example.com/1?a=b" {
		t.Fatalf("meta=%v err=%v", meta, err)
	}
	if got := (Blocker{Meta: meta}).MetaPairs(); len(got) != 2 || got[0] != "pr=https://example.com/1?a=b" {
		t.Fatalf("pairs = %v", got)
	}
	for _, bad := range [][]string{{"novalue"}, {"a=1", "a=2"}} {
		if _, err := ParseBlockerMeta(bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}

func TestBlockerShortPrefersLabelThenCompactCondition(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) *time.Time { value := now.Add(offset); return &value }
	names := func(id string) string { return map[string]string{"01KZ": "api-schema"}[id] }
	for _, tc := range []struct {
		blocker Blocker
		want    string
	}{
		{Blocker{Kind: BlockerExternal, Event: "activity on acme/api#1234", Label: "#1234"}, "#1234"},
		{Blocker{Kind: BlockerExternal, Event: "security review"}, "security review"},
		{Blocker{Kind: BlockerItem, ItemID: "01KZ"}, "on api-schema"},
		{Blocker{Kind: BlockerItem, ItemID: "01XX"}, "on 01XX"},
		{Blocker{Kind: BlockerTime, Until: at(3 * time.Hour)}, "until 12:00"},
		{Blocker{Kind: BlockerTime, Until: at(26 * time.Hour)}, "until Sat 11:00"},
		{Blocker{Kind: BlockerTime, Until: at(10 * 24 * time.Hour)}, "until Oct 26"},
	} {
		if got := tc.blocker.Short(now, names); got != tc.want {
			t.Fatalf("%+v: got %q want %q", tc.blocker, got, tc.want)
		}
	}
	if got := ShortList([]Blocker{{Kind: BlockerExternal, Event: "a"}, {Kind: BlockerExternal, Event: "b", Label: "#2"}}, now, nil); got != "a, #2" {
		t.Fatalf("list = %q", got)
	}
	if err := ValidateBlocker(Blocker{Kind: BlockerExternal, Event: "x", Label: strings.Repeat("y", 41)}, "01KZYHGDCECSFS4BJ2SNTQP49V", now); err == nil {
		t.Fatal("long label accepted")
	}
}
