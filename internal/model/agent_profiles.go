package model

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// AgentProfile names a coding agent setup: a driver and the settings it
// launches with, such as the executable, model and reasoning level. Items
// record the profile they use by name. Authentication stays with the agent.
type AgentProfile struct {
	Name   string         `json:"name"`
	Driver string         `json:"driver"`
	Label  string         `json:"label,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

// AgentProfiles is every agent profile and the default one, kept by the
// daemon. Revision fences a change made from an older read.
type AgentProfiles struct {
	Revision uint64         `json:"revision"`
	Default  string         `json:"default"`
	Profiles []AgentProfile `json:"profiles"`
	// TitleModel is the model that writes item titles; without it the
	// default profile's model does.
	TitleModel *ModelChoice `json:"title_model,omitempty"`
}

// ModelChoice names a model by the driver that runs it, with a reasoning
// level; empty fields take the driver's defaults. It is a bare model, not an
// agent setup: wi calls it without tools, workspace or conversation.
type ModelChoice struct {
	Driver    string `json:"driver"`
	Model     string `json:"model,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

// Validate checks a choice has a driver and short single-line values.
func (c ModelChoice) Validate() error {
	if c.Driver == "" || len(c.Driver) > 64 {
		return fmt.Errorf("a model choice needs a driver")
	}
	for _, value := range []string{c.Driver, c.Model, c.Reasoning} {
		if len(value) > 200 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("invalid model choice")
		}
	}
	return nil
}

var agentProfileName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// Find returns the named profile.
func (p AgentProfiles) Find(name string) (AgentProfile, bool) {
	at := slices.IndexFunc(p.Profiles, func(profile AgentProfile) bool { return profile.Name == name })
	if at < 0 {
		return AgentProfile{}, false
	}
	return p.Profiles[at], true
}

// Validate checks a profile on its own: a lowercase name, a driver, a short
// label, and when set an executable that is an absolute path to an existing
// executable file on this machine.
func (p AgentProfile) Validate() error {
	if !agentProfileName.MatchString(p.Name) || p.Name == "default" {
		return fmt.Errorf("agent profile name %q must be lowercase letters, digits and '-', starting with a letter, and not \"default\"", p.Name)
	}
	if p.Driver == "" || len(p.Driver) > 64 {
		return fmt.Errorf("agent profile %s needs a driver", p.Name)
	}
	if len([]rune(p.Label)) > 80 {
		return fmt.Errorf("agent profile %s label is too long", p.Name)
	}
	if value, ok := p.Config["executable"]; ok {
		path, ok := value.(string)
		if !ok || !filepath.IsAbs(path) {
			return fmt.Errorf("agent profile %s: executable must be an absolute path", p.Name)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("agent profile %s: executable %s: %w", p.Name, path, err)
		}
		if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("agent profile %s: %s is not an executable file", p.Name, path)
		}
	}
	return nil
}
