package options

import "testing"

func TestRegisteredOptionsValidateScopesAndPreserveExplicitOverrides(t *testing.T) {
	r, err := New(Definition{Name: "sample", Options: []Option{{Name: "load", Type: "bool", Default: true, Repository: true}, {Name: "secret", Type: "string", Default: "hidden", Secret: true}, {Name: "labels", Type: "strings", Default: []string{"default"}, Repository: true}}})
	if err != nil {
		t.Fatal(err)
	}
	global := map[string]map[string]any{"sample": {"load": true, "labels": []string{"global"}}}
	repo := map[string]map[string]any{"sample": {"load": false, "labels": []string{}}}
	if _, err := r.Validate(repo, true); err != nil {
		t.Fatal(err)
	}
	v := r.Resolve(global, repo)
	if v["sample"]["load"] != false || len(v["sample"]["labels"].([]string)) != 0 {
		t.Fatal(v)
	}
	if _, err := r.Validate(map[string]map[string]any{"sample": {"secret": "repo"}}, true); err == nil {
		t.Fatal("accepted global-only option")
	}
	for _, v := range []map[string]map[string]any{{"sample": {"load": "true"}}, {"sample": {"unknown": true}}, {"missing": {"load": true}}} {
		if _, err := r.Validate(v, false); err == nil {
			t.Fatal(v)
		}
	}
	values := r.Resolve(global, nil)
	values["sample"]["labels"].([]string)[0] = "changed"
	if global["sample"]["labels"].([]string)[0] != "global" {
		t.Fatal("resolution mutated source")
	}
	defs := r.Definitions()
	defs[0].Options[0].Name = "changed"
	if r.Definitions()[0].Options[0].Name != "load" {
		t.Fatal("mutable schema leaked")
	}
	redacted := map[string]any{"sample": map[string]any{"load": false, "secret": "value"}, "unknown": map[string]any{"api_key": "value"}}
	r.Redact(redacted)
	if redacted["sample"].(map[string]any)["secret"] != "[redacted]" || redacted["sample"].(map[string]any)["load"] != false || redacted["unknown"].(map[string]any)["api_key"] != "[redacted]" {
		t.Fatal(redacted)
	}
}
func TestIntegerDefaultsKeepTheirPrecision(t *testing.T) {
	r, err := New(Definition{Name: "sample", Options: []Option{{Name: "count", Type: "integer", Default: int64(9007199254740993)}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Resolve(nil, nil)["sample"]["count"] != int64(9007199254740993) {
		t.Fatal("integer default lost precision")
	}
}

func TestRegistryRejectsDuplicateDeclarations(t *testing.T) {
	d := Definition{Name: "sample", Options: []Option{{Name: "load", Type: "bool", Default: true}}}
	if _, err := New(d, d); err == nil {
		t.Fatal("duplicate extension")
	}
	d.Options = append(d.Options, d.Options[0])
	if _, err := New(d); err == nil {
		t.Fatal("duplicate option")
	}
}
