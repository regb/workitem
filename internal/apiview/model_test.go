package apiview

import (
	"encoding/json"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestEmptyListsEncodeAsArraysNotNull(t *testing.T) {
	encoded, _ := json.Marshal(struct {
		Labels   []string `json:"labels"`
		Warnings []string `json:"warnings"`
	}{Strings(nil), Runtime("item", nil).Warnings})
	if string(encoded) != `{"labels":[],"warnings":[]}` {
		t.Fatalf("encoded %s", encoded)
	}
	if got := Manifest(&v1.Item{Id: "item"}).Labels; got == nil {
		t.Fatal("manifest labels are nil")
	}
}

func TestImplicitTerminalControlNamesTheTerminalAsHolder(t *testing.T) {
	status, _ := Control("item", &v1.ControlStatus{Implicit: true}, nil)
	if status.Holder != "terminal" {
		t.Fatalf("holder %q", status.Holder)
	}
	status, _ = Control("item", &v1.ControlStatus{Lease: &v1.ControlLease{Holder: "web"}}, nil)
	if status.Holder != "web" {
		t.Fatalf("lease holder %q", status.Holder)
	}
	if status, _ = Control("item", &v1.ControlStatus{}, nil); status.Holder != "" {
		t.Fatalf("vacant holder %q", status.Holder)
	}
}
