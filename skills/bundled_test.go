package skills

import (
	"bytes"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"os"
	"testing"
)

func TestBundledSkillMatchesSource(t *testing.T) {
	bundles := Bundled()
	if err := protocol.ValidateSkills(bundles); err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 1 || bundles[0].Name != "wi" {
		t.Fatalf("bundles=%v", bundles)
	}
	source, err := os.ReadFile("wi/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range bundles[0].Files {
		if f.Path == "SKILL.md" {
			if !bytes.Equal(f.Data, source) {
				t.Fatal("embedded skill differs from source")
			}
			return
		}
	}
	t.Fatal("no SKILL.md")
}

func TestPluginSectionsExtendOnlyTheWiSkill(t *testing.T) {
	base := Bundled()
	extended := WithPluginSections(base, []PluginSection{{Name: "github", Markdown: "Use `wi wait --for <PR URL>`.\n"}})
	if err := protocol.ValidateSkills(extended); err != nil {
		t.Fatal(err)
	}
	var skill []byte
	for _, f := range extended[0].Files {
		if f.Path == "SKILL.md" {
			skill = f.Data
		}
	}
	if !bytes.Contains(skill, []byte("### Plugin: github\n\nUse `wi wait --for <PR URL>`.\n")) {
		t.Fatalf("plugin section missing:\n%s", skill)
	}
	for _, f := range base[0].Files {
		if f.Path == "SKILL.md" && bytes.Contains(f.Data, []byte("Plugin: github")) {
			t.Fatal("composition mutated the embedded skill")
		}
	}
	if got := WithPluginSections(base, nil); len(got) != len(base) {
		t.Fatal("no sections changed the skill set")
	}
}
