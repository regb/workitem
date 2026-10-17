// Package skills supplies the trusted skill resources shipped with this build.
package skills

import (
	"embed"
	"io/fs"
	"sort"
	"strings"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

//go:embed wi
var bundled embed.FS

func Bundled() []driverprotocol.Skill {
	skill := driverprotocol.Skill{Name: "wi"}
	// The embedded tree is fixed at compile time and contains no symlinks.
	if err := fs.WalkDir(bundled, "wi", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := bundled.ReadFile(p)
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(p, "wi/")
		skill.Files = append(skill.Files, driverprotocol.SkillFile{Path: relative, Data: data, Executable: strings.HasPrefix(relative, "scripts/")})
		return nil
	}); err != nil {
		panic(err)
	}
	sort.Slice(skill.Files, func(i, j int) bool { return skill.Files[i].Path < skill.Files[j].Path })
	return []driverprotocol.Skill{skill}
}

// PluginSection is Markdown an enabled plugin contributes to the wi skill.
type PluginSection struct {
	Name     string
	Markdown string
}

// WithPluginSections returns the bundled skills with each plugin section
// appended to the wi skill's SKILL.md, so agents learn only the plugins that
// are enabled for this launch.
func WithPluginSections(bundles []driverprotocol.Skill, sections []PluginSection) []driverprotocol.Skill {
	if len(sections) == 0 {
		return bundles
	}
	var extra strings.Builder
	extra.WriteString("\n## Enabled plugins\n\nThese plugins are enabled in this wi installation. Their instructions follow.\n")
	for _, section := range sections {
		extra.WriteString("\n### Plugin: " + section.Name + "\n\n")
		extra.WriteString(strings.TrimSpace(section.Markdown))
		extra.WriteString("\n")
	}
	result := make([]driverprotocol.Skill, len(bundles))
	for i, bundle := range bundles {
		result[i] = driverprotocol.Skill{Name: bundle.Name, Files: append([]driverprotocol.SkillFile(nil), bundle.Files...)}
		if bundle.Name != "wi" {
			continue
		}
		for j, file := range result[i].Files {
			if file.Path == "SKILL.md" {
				data := append(append([]byte(nil), file.Data...), []byte(extra.String())...)
				result[i].Files[j] = driverprotocol.SkillFile{Path: file.Path, Data: data, Executable: file.Executable}
			}
		}
	}
	return result
}
