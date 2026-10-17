package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/regb/workitem/internal/options"
)

// RegistrationKeys are the installation fields of a [plugins.NAME] table.
// Every other key in that table is one of the plugin's registered options.
var RegistrationKeys = map[string]bool{"enabled": true, "command": true, "manifest": true}

// Registration contains installation metadata only. Behavior belongs to the
// shared option schema in the same table, never an opaque nested table.
type Registration struct {
	Enabled  bool     `json:"enabled"`
	Command  []string `json:"command,omitempty"`
	Manifest string   `json:"manifest,omitempty"`
	// EnabledSet records an explicit enabled key, so a required plugin can
	// reject enabled = false instead of silently ignoring it.
	EnabledSet bool `json:"-"`
}

func (r *Registration) UnmarshalJSON(data []byte) error {
	*r = Registration{}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key, v := range fields {
		switch key {
		case "enabled":
			if err := json.Unmarshal(v, &r.Enabled); err != nil {
				return fmt.Errorf("enabled must be boolean")
			}
			r.EnabledSet = true
		case "manifest":
			if err := json.Unmarshal(v, &r.Manifest); err != nil {
				return fmt.Errorf("manifest must be a path")
			}
		case "command":
			var single string
			if json.Unmarshal(v, &single) == nil {
				r.Command = []string{single}
			} else if err := json.Unmarshal(v, &r.Command); err != nil {
				return fmt.Errorf("command must be a string or array of strings")
			}
		default:
			// Behavior options are validated against the registered schema.
		}
	}
	return nil
}

type Job struct {
	Name           string   `json:"name"`
	Args           []string `json:"args,omitempty"`
	IntervalOption string   `json:"interval_option"`
	Repository     bool     `json:"repository"`
}
type Definition struct {
	Name            string               `json:"name"`
	Options         []options.Option     `json:"options"`
	Commands        []string             `json:"commands,omitempty"`
	Jobs            []Job                `json:"jobs,omitempty"`
	Command         []string             `json:"command,omitempty"`
	Instructions    string               `json:"-"`
	Native          func(Approver) Hooks `json:"-"`
	Required        bool                 `json:"-"`
	RetiredSections []string             `json:"-"`
}
type Registry struct {
	definitions map[string]Definition
	settings    *options.Registry
}

func NewRegistry(definitions ...Definition) (*Registry, error) {
	r := &Registry{definitions: map[string]Definition{}}
	schemas := []options.Definition{}
	for _, d := range definitions {
		d = cloneDefinition(d)
		if !validName(d.Name) {
			return nil, fmt.Errorf("invalid plugin name %q", d.Name)
		}
		if reserved(d.Name) {
			return nil, fmt.Errorf("plugin %q collides with a built-in command", d.Name)
		}
		if _, exists := r.definitions[d.Name]; exists {
			return nil, fmt.Errorf("duplicate plugin %q", d.Name)
		}
		if (len(d.Commands) > 0 || len(d.Jobs) > 0) && len(d.Command) == 0 {
			return nil, fmt.Errorf("plugin %s jobs/commands require an executable", d.Name)
		}
		if d.Native == nil && len(d.Command) == 0 && d.Instructions == "" {
			return nil, fmt.Errorf("plugin %s has no implementation or instruction assets", d.Name)
		}
		if !utf8.ValidString(d.Instructions) || strings.ContainsRune(d.Instructions, 0) {
			return nil, fmt.Errorf("plugin instructions must be UTF-8 text without NUL")
		}
		if len(d.Instructions) > MaxSkillBytes {
			return nil, fmt.Errorf("plugin instructions exceed %d bytes", MaxSkillBytes)
		}
		for _, o := range d.Options {
			if RegistrationKeys[o.Name] {
				return nil, fmt.Errorf("plugin %s option %q collides with a [plugins.%s] registration field", d.Name, o.Name, d.Name)
			}
		}
		seen := map[string]bool{}
		for _, cmd := range d.Commands {
			if !validName(cmd) || seen[cmd] {
				return nil, fmt.Errorf("invalid or duplicate plugin command")
			}
			seen[cmd] = true
		}
		seen = map[string]bool{}
		for _, job := range d.Jobs {
			if !validName(job.Name) || seen[job.Name] {
				return nil, fmt.Errorf("invalid or duplicate plugin job")
			}
			seen[job.Name] = true
			found := false
			for i, o := range d.Options {
				if o.Name == job.IntervalOption && o.Type == "integer" && !o.Repository {
					found = true
					min, max := int64(30), int64(86400)
					if o.Min == nil || *o.Min < min {
						d.Options[i].Min = &min
					}
					if o.Max == nil || *o.Max > max {
						d.Options[i].Max = &max
					}
				}
			}
			if !found {
				return nil, fmt.Errorf("job %s requires a global integer interval option", job.Name)
			}
		}
		if len(d.Command) > 0 && strings.TrimSpace(d.Command[0]) == "" {
			return nil, fmt.Errorf("empty plugin executable")
		}
		r.definitions[d.Name] = d
		schemas = append(schemas, options.Definition{Name: d.Name, Options: d.Options, RetiredSections: d.RetiredSections})
	}
	var err error
	r.settings, err = options.New(schemas...)
	if err != nil {
		return nil, err
	}
	return r, nil
}
func reserved(name string) bool {
	if strings.HasPrefix(name, "internal-") {
		return true
	}
	switch name {
	case "repo", "new", "list", "show", "events", "merge", "state", "attention", "workspace", "terminal", "start", "resume", "shelve", "close", "wait", "pick", "blocker", "plugin", "delete", "shutdown", "agent", "switch", "next", "config", "init", "info", "daemon", "help", "version", "internal-driver":
		return true
	}
	return false
}
func cloneDefinition(d Definition) Definition {
	d.Command = append([]string(nil), d.Command...)
	d.Commands = append([]string(nil), d.Commands...)
	d.RetiredSections = append([]string(nil), d.RetiredSections...)
	d.Options = append([]options.Option(nil), d.Options...)
	for i, o := range d.Options {
		d.Options[i].Default = options.CloneValues(map[string]any{"v": o.Default})["v"]
		d.Options[i].Choices = append([]string(nil), o.Choices...)
		if o.Min != nil {
			v := *o.Min
			d.Options[i].Min = &v
		}
		if o.Max != nil {
			v := *o.Max
			d.Options[i].Max = &v
		}
	}
	d.Jobs = append([]Job(nil), d.Jobs...)
	for i, j := range d.Jobs {
		d.Jobs[i].Args = append([]string(nil), j.Args...)
	}
	return d
}

func validName(name string) bool {
	if name == "" || len(name) > 64 || !((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= '0' && name[0] <= '9')) {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
func (r *Registry) Options() *options.Registry { return r.settings }
func (r *Registry) Definitions() []Definition {
	result := []Definition{}
	if r == nil {
		return result
	}
	for _, d := range r.definitions {
		result = append(result, cloneDefinition(d))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// LoadManifest reads declarative metadata and a bounded, contained static
// instruction asset. Neither schema inspection nor registration executes code.
func LoadManifest(path string) (Definition, error) {
	var manifest struct {
		Version      int              `json:"version"`
		Name         string           `json:"name"`
		Options      []options.Option `json:"options"`
		Commands     []string         `json:"commands"`
		Jobs         []Job            `json:"jobs"`
		Command      []string         `json:"command"`
		Instructions string           `json:"instructions"`
	}
	raw, err := readRegular(path, 64<<10)
	if err != nil {
		return Definition{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Definition{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Definition{}, fmt.Errorf("manifest must contain one JSON value")
	}
	if manifest.Version != 1 {
		return Definition{}, fmt.Errorf("unsupported plugin manifest version %d", manifest.Version)
	}
	d := Definition{Name: manifest.Name, Options: manifest.Options, Commands: manifest.Commands, Jobs: manifest.Jobs, Command: manifest.Command}
	root, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return d, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return d, err
	}
	if len(d.Command) > 0 && !filepath.IsAbs(d.Command[0]) {
		d.Command[0] = filepath.Join(root, d.Command[0])
	}
	if manifest.Instructions != "" {
		rel := filepath.Clean(manifest.Instructions)
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return d, fmt.Errorf("instruction asset must remain inside the plugin package")
		}
		asset := filepath.Join(root, rel)
		canonical, err := filepath.EvalSymlinks(asset)
		if err != nil {
			return d, err
		}
		if canonical != asset {
			return d, fmt.Errorf("instruction asset must not use symlinks")
		}
		data, err := readRegular(asset, MaxSkillBytes)
		if err != nil {
			return d, err
		}
		d.Instructions = string(data)
	}
	return d, nil
}
func readRegular(path string, limit int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin metadata/assets must be regular files")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return nil, fmt.Errorf("plugin metadata changed during inspection")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit+1)))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("plugin metadata/asset exceeds size limit")
	}
	return data, nil
}
