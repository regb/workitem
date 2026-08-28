package model_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Fix refresh-token race":      "fix-refresh-token-race",
		"  multiple___separators... ": "multiple-separators",
		"!!!":                         "item",
	}
	for input, want := range tests {
		if got := model.Slugify(input); got != want {
			t.Fatalf("Slugify(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeSlugSelector(t *testing.T) {
	for input, want := range map[string]string{"DEMO-42": "demo-42", "42": "42", "scheduler-upgrade": "scheduler-upgrade"} {
		got, ok := model.NormalizeSlugSelector(input)
		if !ok || got != want {
			t.Fatalf("NormalizeSlugSelector(%q) = %q, %v", input, got, ok)
		}
	}
	for _, input := range []string{"", "scheduler upgrade", "scheduler/upgrade", "_invalid"} {
		if got, ok := model.NormalizeSlugSelector(input); ok {
			t.Fatalf("NormalizeSlugSelector(%q) = %q, true", input, got)
		}
	}
}

func TestNewIDSortableAndValid(t *testing.T) {
	t1 := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Millisecond)
	id1, err := model.NewIDWith(t1, bytes.NewReader(bytes.Repeat([]byte{0x01}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := model.NewIDWith(t2, bytes.NewReader(bytes.Repeat([]byte{0x00}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if !model.ValidID(id1) || !model.ValidID(id2) {
		t.Fatalf("generated invalid ids: %q %q", id1, id2)
	}
	if len(id1) != 26 {
		t.Fatalf("id length = %d", len(id1))
	}
	if strings.Compare(id1, id2) >= 0 {
		t.Fatalf("ids are not time-sortable: %s >= %s", id1, id2)
	}
}

func TestUniqueIDPrefixes(t *testing.T) {
	ids := []string{
		"01KYTAXAJ97CYKWY9XF9Y2SCJ0",
		"01KYTAM0AZBTR8NRVVTTZJQJRQ",
		"01KYT4ABCDE000000000000000",
	}
	prefixes := model.UniqueIDPrefixes(ids, 6)
	if prefixes[ids[0]] != "01KYTAX" {
		t.Fatalf("prefix for first id = %q", prefixes[ids[0]])
	}
	if prefixes[ids[1]] != "01KYTAM" {
		t.Fatalf("prefix for second id = %q", prefixes[ids[1]])
	}
	if prefixes[ids[2]] != "01KYT4" {
		t.Fatalf("prefix for third id = %q", prefixes[ids[2]])
	}
}

func TestNormalizeLabels(t *testing.T) {
	got, err := model.NormalizeLabel(" Needs Review ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "needs-review" {
		t.Fatalf("label = %q", got)
	}
	if _, err := model.NormalizeLabel("bad:label"); err == nil {
		t.Fatal("expected unsafe label to fail")
	}
	labels, added, err := model.AddLabels([]string{"backend"}, "Auth", "backend")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(labels, ",") != "backend,auth" || strings.Join(added, ",") != "auth" {
		t.Fatalf("labels=%v added=%v", labels, added)
	}
}

func TestNewManifestUsesEmptyLabelsAndNoPiRegistry(t *testing.T) {
	m := model.NewManifest("01K1ABCDE0000000000000000", "slug", "Title", nil, false, time.Now(), model.Repository{}, model.Checkout{})
	if m.Labels == nil {
		t.Fatalf("manifest labels should be a non-nil empty slice: %+v", m)
	}
	if m.RootPiSession != nil {
		t.Fatalf("new manifest should not pre-register a root Pi session: %+v", m)
	}
}

func TestNames(t *testing.T) {
	id := "01K1ABCDE0000000000000000"
	if got := model.ItemBranchName("Fix Race", id); got != "wi/fix-race-00000000" {
		t.Fatalf("ItemBranchName = %q", got)
	}
	if got := model.TerminalSessionName(id); got != "wi-01K1ABCDE000" {
		t.Fatalf("TmuxSessionName = %q", got)
	}
}
