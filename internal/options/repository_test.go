package options

import (
	"encoding/json"
	"testing"
)

func TestRepositorySettingsAreValidatedAndUnavailableNamespacesInactive(t *testing.T) {
	r, err := New(Definition{Name: "sample", Options: []Option{{Name: "load", Type: "bool", Default: true, Repository: true}, {Name: "secret", Type: "string", Default: "global", Secret: true, Repository: true}, {Name: "global-only", Type: "string", Default: "value"}, {Name: "count", Type: "integer", Default: int64(0), Repository: true}, {Name: "labels", Type: "strings", Default: []string{}, Repository: true}}})
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]map[string]json.RawMessage{"extensions.sample": {"load": json.RawMessage(`false`), "secret": json.RawMessage(`"private"`)}, "extensions.unavailable": {"load": json.RawMessage(`true`), "secret": json.RawMessage(`"unknown-private"`)}}
	effective, warnings, err := r.ResolveRepository(nil, stored)
	if err != nil || len(warnings) != 1 || effective["sample"]["load"] != false || effective["unavailable"] != nil {
		t.Fatal(effective, warnings, err)
	}
	hidden := r.RedactRepository(stored)
	if string(hidden["extensions.sample"]["secret"]) != `"[redacted]"` || string(hidden["extensions.unavailable"]["secret"]) != `"[redacted]"` || string(stored["extensions.sample"]["secret"]) != `"private"` {
		t.Fatal("redaction mutated stored state or exposed secrets")
	}
	if _, err := r.ParseRepositoryValue("extensions.sample", "global-only", "override"); err == nil {
		t.Fatal("overrode global-only setting")
	}
	for _, input := range []struct{ key, text, want string }{{"count", "+12", "12"}, {"count", "9007199254740993", "9007199254740993"}, {"labels", `["one","two"]`, `["one","two"]`}, {"secret", "", `""`}, {"load", "false", "false"}} {
		raw, err := r.ParseRepositoryValue("extensions.sample", input.key, input.text)
		if err != nil || string(raw) != input.want {
			t.Fatal(input, string(raw), err)
		}
	}
	for _, path := range []string{"trust", "direnv.trust", "extensions.sample.", "extensions.sample.count.child", "extensions../key"} {
		if _, _, err := SplitRepositoryKey(path); err == nil {
			t.Fatal("invalid setting path accepted", path)
		}
	}
	stored["extensions.sample"]["load"] = json.RawMessage(`"wrong-type"`)
	if _, _, err := r.ResolveRepository(nil, stored); err == nil {
		t.Fatal("invalid stored setting became active")
	}
}
