package cli

import (
	"encoding/json"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestCLISettingKeepsEmptyListsAsArrays(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *v1.StringList
	}{
		{"omitted values", &v1.StringList{}},
		{"empty values", &v1.StringList{Values: []string{}}},
		{"nil message", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := &v1.SettingValue{Value: &v1.SettingValue_ListValue{ListValue: tc.value}}
			raw, err := json.Marshal(cliSettingAny(value))
			if err != nil || string(raw) != "[]" {
				t.Fatalf("empty list became %s: %v", raw, err)
			}
		})
	}
	var value v1.SettingValue
	if err := protojson.Unmarshal([]byte(`{"list_value":{}}`), &value); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cliSettingAny(&value))
	if err != nil || string(raw) != "[]" {
		t.Fatalf("API empty list became %s: %v", raw, err)
	}
}

func TestCLISettingCopiesNonemptyStringLists(t *testing.T) {
	list := &v1.StringList{Values: []string{"local.json"}}
	value := &v1.SettingValue{Value: &v1.SettingValue_ListValue{ListValue: list}}
	converted := cliSettingAny(value).([]string)
	if len(converted) != 1 || converted[0] != "local.json" {
		t.Fatal(converted)
	}
	converted[0] = "changed"
	if list.Values[0] != "local.json" {
		t.Fatal("conversion mutated the response")
	}
}
