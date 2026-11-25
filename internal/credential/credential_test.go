package credential

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const item = "01M86SNFTN3MVQZN1JAVGSRZQK"

func TestMintAndParse(t *testing.T) {
	for _, test := range []struct {
		kind Kind
		item string
	}{{Operator, ""}, {Proxy, ""}, {Item, item}} {
		value, err := New(test.kind, test.item)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(value.Token())
		if err != nil || parsed.Kind != test.kind || parsed.ItemID != test.item || !parsed.Matches(value.Hash()) {
			t.Fatalf("%s: %+v %v", test.kind, parsed, err)
		}
		other, _ := New(test.kind, test.item)
		if other.Matches(value.Hash()) {
			t.Fatalf("%s: two credentials match", test.kind)
		}
	}
	if !strings.HasPrefix(mustNew(t, Item, item).Token(), "wi_item_"+item+"_") {
		t.Fatal("item credential does not name its item")
	}
}

func TestParseRefusesForeignText(t *testing.T) {
	good := mustNew(t, Item, item).Token()
	for _, token := range []string{"", "Bearer x", "wi_operator", "wi_operator_short", "wi_root_" + strings.Repeat("A", 43), "wi_item_" + strings.Repeat("A", 43), "wi_item_bad_" + good[len(good)-43:], good + "x", strings.Repeat("w", 300)} {
		if _, err := Parse(token); err == nil {
			t.Fatalf("accepted %q", token)
		}
	}
	if _, err := New(Item, "not-an-id"); err == nil {
		t.Fatal("minted an item credential for an invalid id")
	}
	if _, err := New(Operator, item); err == nil {
		t.Fatal("minted an operator credential naming an item")
	}
}

func TestFilesAreOwnerOnlyAndStable(t *testing.T) {
	root := t.TempDir()
	path := OperatorFile(root)
	first, err := Ensure(path, Operator)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}
	again, err := Ensure(path, Operator)
	if err != nil || again.Token() != first.Token() {
		t.Fatal("an existing credential was replaced")
	}
	// A file holding another kind, or garbage, is replaced.
	if _, err := Read(path, Proxy); err == nil {
		t.Fatal("read an operator credential as a proxy one")
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaced, err := Ensure(path, Operator)
	if err != nil || replaced.Token() == first.Token() {
		t.Fatalf("garbage was not replaced: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func mustNew(t *testing.T, kind Kind, itemID string) Credential {
	t.Helper()
	value, err := New(kind, itemID)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
