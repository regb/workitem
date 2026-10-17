package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/options"
)

func TestCatalogValidatesContributionsAndProtectsItsSnapshot(t *testing.T) {
	original := Definition{Name: "demo", Command: []string{"executable"}, Commands: []string{"wait"}, Options: []options.Option{{Name: "labels", Type: "strings", Default: []string{"one"}}}}
	registry, err := NewRegistry(original)
	if err != nil {
		t.Fatal(err)
	}
	original.Command[0] = "mutated"
	original.Options[0].Default.([]string)[0] = "mutated"
	view := registry.Definitions()
	view[0].Commands[0] = "mutated"
	view[0].Options[0].Default.([]string)[0] = "mutated"
	after := registry.Definitions()[0]
	if after.Command[0] != "executable" || after.Commands[0] != "wait" || after.Options[0].Default.([]string)[0] != "one" {
		t.Fatal("catalog aliases its callers")
	}
	for _, bad := range []Definition{
		{Name: "list", Command: []string{"x"}},
		{Name: "bad/name", Command: []string{"x"}},
		{Name: "demo", Command: []string{"x"}, Commands: []string{"wait", "wait"}},
		{Name: "demo", Command: []string{"x"}, Jobs: []Job{{Name: "check", IntervalOption: "missing"}}},
		{Name: "demo", Command: []string{"x"}, Instructions: strings.Repeat("x", MaxSkillBytes+1)},
		{Name: "demo", Command: []string{"x"}, Options: []options.Option{{Name: "enabled", Type: "bool", Default: false}}},
		{Name: "demo", Command: []string{"x"}, Options: []options.Option{{Name: "manifest", Type: "string", Default: ""}}},
	} {
		if _, err := NewRegistry(bad); err == nil {
			t.Fatalf("accepted malformed definition %s", bad.Name)
		}
	}
	if _, err := NewRegistry(after, after); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if _, err := NewRegistry(Definition{Name: "assets", Instructions: "Static only."}); err != nil {
		t.Fatal("instruction-only plugin required a process", err)
	}
}
func TestManifestIsDeclarativeBoundedAndPackageContained(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plugin.json")
	asset := filepath.Join(root, "instructions.md")
	if err := os.WriteFile(asset, []byte("Static instructions."), 0600); err != nil {
		t.Fatal(err)
	}
	valid := `{"version":1,"name":"demo","command":["bin/plugin"],"commands":["wait"],"instructions":"instructions.md","options":[{"name":"count","type":"integer","default":9007199254740993}]}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Command[0] != filepath.Join(root, "bin/plugin") || d.Instructions != "Static instructions." || d.Options[0].Default.(json.Number).String() != "9007199254740993" {
		t.Fatal("manifest lost path, content or numeric precision")
	}
	for _, bad := range []string{strings.Replace(valid, `"version":1`, `"version":2`, 1), valid + ` {}`, strings.Replace(valid, `"instructions.md"`, `"../outside.md"`, 1), strings.Replace(valid, `"name":"demo"`, `"name":"demo","unknown":true`, 1), strings.Repeat("x", (64<<10)+1)} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(path); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(asset); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "plugin.json"), asset); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("symlink instruction asset accepted")
	}
	if err := os.Remove(asset); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte(strings.Repeat("x", MaxSkillBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("oversized asset accepted")
	}
}
func TestNativeAndExecutableUseOneSettingsResolver(t *testing.T) {
	r, err := NewRegistry(Definition{Name: "native", Required: true, Native: func(Approver) Hooks {
		return Hooks{Sanitize: func(_ context.Context, _ EnvironmentRequest, v map[string]any) (Patch, error) {
			if v["load"] != false {
				return Patch{}, os.ErrInvalid
			}
			return Patch{Unset: []string{"OLD_SECRET"}}, nil
		}}
	}, Options: []options.Option{{Name: "load", Type: "bool", Default: true, Repository: true}}}, Definition{Name: "external", Command: []string{"not-executed"}, Commands: []string{"run"}, Options: []options.Option{{Name: "labels", Type: "strings", Default: []string{"default"}, Repository: true}}})
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	reader := func(_ context.Context, id string) (model.RepositorySettings, error) {
		reads++
		return model.RepositorySettings{RepositoryID: id, Revision: 1, Namespaces: map[string]map[string]json.RawMessage{"plugins.native": {"load": json.RawMessage(`false`)}, "plugins.external": {"labels": json.RawMessage(`[]`)}}}, nil
	}
	h := NewHost(r, map[string]Registration{"external": {Enabled: true}}, nil, "wi", reader)
	env, _, _, err := h.Preparation(nil).Environment(context.Background(), "sanitize", EnvironmentRequest{Subject: Subject{RepositoryID: "repo"}, Values: map[string]string{"OLD_SECRET": "private"}})
	if err != nil || env["OLD_SECRET"] != "" {
		t.Fatal("native preparation did not receive repository overrides", err)
	}
	specs, err := h.Specs(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range specs {
		if s.Name == "external" && len(s.Settings["labels"].([]any)) != 0 {
			t.Fatal("executable did not receive explicit empty override")
		}
	}
	if reads != 1 {
		t.Fatal("one operation did not freeze its repository snapshot", reads)
	}
	if _, err := h.Fresh().Specs(context.Background(), "repo"); err != nil || reads != 2 {
		t.Fatal("next operation did not refresh policy", reads, err)
	}
}
func TestJobsRefreshPerRepositorySettings(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "seen")
	script := filepath.Join(root, "plugin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|%s\\n' \"$WI_PLUGIN_REPOSITORY_ID\" \"$WI_PLUGIN_CONFIG\" >> \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(Definition{Name: "demo", Command: []string{script, output}, Jobs: []Job{{Name: "poll", IntervalOption: "interval_seconds", Repository: true}}, Options: []options.Option{{Name: "interval_seconds", Type: "integer", Default: int64(30)}, {Name: "flag", Type: "bool", Default: true, Repository: true}}})
	if err != nil {
		t.Fatal(err)
	}
	flag := false
	h := NewHost(r, map[string]Registration{"demo": {Enabled: true}}, nil, "wi", func(_ context.Context, id string) (model.RepositorySettings, error) {
		raw, _ := json.Marshal(flag)
		return model.RepositorySettings{RepositoryID: id, Namespaces: map[string]map[string]json.RawMessage{"plugins.demo": {"flag": raw}}}, nil
	})
	h.ListRepositories = func(context.Context) ([]string, error) { return []string{"repo-a"}, nil }
	jobs, err := h.JobSpecs(context.Background(), "")
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	runner := Runner{Host: h, WiPath: "wi", LogDir: filepath.Join(root, "logs")}
	runner.runJob(context.Background(), jobs[0])
	flag = true
	runner.runJob(context.Background(), jobs[0])
	seen, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(seen), `repo-a|{"flag":false`) || !strings.Contains(string(seen), `repo-a|{"flag":true`) {
		t.Fatal("job ignored repository policy or retained a stale snapshot", err)
	}
}
