// Package options describes extension configuration without loading extension code.
package options

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

type Option struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Default     any      `json:"default"`
	Repository  bool     `json:"repository"`
	Secret      bool     `json:"secret"`
	Description string   `json:"description"`
	Choices     []string `json:"choices,omitempty"`
}
type Definition struct {
	RetiredSections []string `json:"retired_sections,omitempty"`
	Name            string   `json:"name"`
	Options         []Option `json:"options"`
}
type Registry struct{ definitions map[string]Definition }

func New(definitions ...Definition) (*Registry, error) {
	r := &Registry{definitions: map[string]Definition{}}
	for _, d := range definitions {
		if !validName(d.Name) {
			return nil, fmt.Errorf("invalid extension name %q", d.Name)
		}
		if _, ok := r.definitions[d.Name]; ok {
			return nil, fmt.Errorf("duplicate extension %q", d.Name)
		}
		seen := map[string]bool{}
		for _, o := range d.Options {
			if !validName(o.Name) || seen[o.Name] {
				return nil, fmt.Errorf("invalid or duplicate option %s.%s", d.Name, o.Name)
			}
			seen[o.Name] = true
			if err := validate(o, o.Default); err != nil {
				return nil, err
			}
		}
		r.definitions[d.Name] = clone(d)
	}
	return r, nil
}
func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
func clone(d Definition) Definition {
	out := d
	out.Options = append([]Option(nil), d.Options...)
	out.RetiredSections = append([]string(nil), d.RetiredSections...)
	for i := range out.Options {
		out.Options[i].Choices = append([]string(nil), out.Options[i].Choices...)
		out.Options[i].Default = CloneValues(map[string]any{"value": d.Options[i].Default})["value"]
	}
	return out
}
func (r *Registry) Definitions() []Definition {
	result := []Definition{}
	if r == nil {
		return result
	}
	for _, d := range r.definitions {
		result = append(result, clone(d))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
func (r *Registry) Validate(settings map[string]map[string]any, repository bool) ([]string, error) {
	warnings := []string{}
	for name, values := range settings {
		var d Definition
		found := false
		if r != nil {
			d, found = r.definitions[name]
		}
		if !found {
			if values["load"] == true || values["enabled"] == true {
				return warnings, fmt.Errorf("extension %q is not registered", name)
			}
			warnings = append(warnings, "unregistered extension settings: "+name)
			continue
		}
		known := map[string]Option{}
		for _, o := range d.Options {
			known[o.Name] = o
		}
		for key, value := range values {
			o, ok := known[key]
			if !ok {
				return warnings, fmt.Errorf("unknown extension option %s.%s", name, key)
			}
			if repository && !o.Repository {
				return warnings, fmt.Errorf("extension option %s.%s is global-only", name, key)
			}
			if err := validate(o, value); err != nil {
				return warnings, fmt.Errorf("extension option %s.%s: %w", name, key, err)
			}
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}
func validate(o Option, value any) error {
	valid := false
	switch o.Type {
	case "string":
		_, valid = value.(string)
	case "bool":
		_, valid = value.(bool)
	case "integer":
		switch n := value.(type) {
		case int, int64:
			valid = true
		case float64:
			valid = n == float64(int64(n))
		case json.Number:
			_, err := n.Int64()
			valid = err == nil
		}
	case "strings":
		v := reflect.ValueOf(value)
		valid = v.IsValid() && v.Kind() == reflect.Slice
		if valid {
			for i := 0; i < v.Len(); i++ {
				if _, ok := v.Index(i).Interface().(string); !ok {
					valid = false
					break
				}
			}
		}
	}
	if !valid {
		return fmt.Errorf("expected %s", o.Type)
	}
	if len(o.Choices) > 0 {
		for _, choice := range o.Choices {
			if value == choice {
				return nil
			}
		}
		return fmt.Errorf("expected one of the registered choices")
	}
	return nil
}

// Resolve replaces individual option values; slices are never implicitly appended.
func (r *Registry) Resolve(global, repo map[string]map[string]any) map[string]map[string]any {
	result := map[string]map[string]any{}
	for _, d := range r.Definitions() {
		result[d.Name] = map[string]any{}
		for _, o := range d.Options {
			result[d.Name][o.Name] = o.Default
		}
	}
	for _, source := range []map[string]map[string]any{global, repo} {
		for name, values := range source {
			if result[name] == nil {
				result[name] = map[string]any{}
			}
			for key, value := range values {
				result[name][key] = value
			}
		}
	}
	for name, values := range result {
		result[name] = CloneValues(values)
	}
	return result
}
func CloneValues(values map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range values {
		switch v := value.(type) {
		case map[string]any:
			result[key] = CloneValues(v)
		case []any:
			a := make([]any, len(v))
			for i, entry := range v {
				a[i] = CloneValues(map[string]any{"value": entry})["value"]
			}
			result[key] = a
		case []string:
			result[key] = append([]string{}, v...)
		default:
			result[key] = v
		}
	}
	return result
}
func (r *Registry) Redact(settings map[string]any) {
	for name, raw := range settings {
		values, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		known := map[string]Option{}
		if r != nil {
			for _, o := range r.definitions[name].Options {
				known[o.Name] = o
			}
		}
		for key := range values {
			if o, ok := known[key]; !ok || o.Secret {
				values[key] = "[redacted]"
			}
		}
	}
}
