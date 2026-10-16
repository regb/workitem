package options

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func SplitRepositoryKey(path string) (string, string, error) {
	parts := strings.Split(path, ".")
	if len(parts) != 3 || parts[0] != "extensions" || !validName(parts[1]) || !validName(parts[2]) || len(parts[1]) > 64 || len(parts[2]) > 64 {
		return "", "", fmt.Errorf("setting key must be extensions.EXTENSION.OPTION")
	}
	return strings.Join(parts[:2], "."), parts[2], nil
}
func (r *Registry) repositoryOption(namespace, key string) (Option, error) {
	if _, _, err := SplitRepositoryKey(namespace + "." + key); err != nil {
		return Option{}, err
	}
	name := strings.TrimPrefix(namespace, "extensions.")
	if r != nil {
		if d, ok := r.definitions[name]; ok {
			for _, o := range d.Options {
				if o.Name == key {
					if !o.Repository {
						return Option{}, fmt.Errorf("setting %s.%s is global-only", namespace, key)
					}
					return o, nil
				}
			}
		}
	}
	return Option{}, fmt.Errorf("repository setting %s.%s is not registered", namespace, key)
}
func (r *Registry) ValidateRepositorySetting(namespace, key string, value any) error {
	o, err := r.repositoryOption(namespace, key)
	if err != nil {
		return err
	}
	if err := validate(o, value); err != nil {
		return fmt.Errorf("setting %s.%s: %w", namespace, key, err)
	}
	return nil
}
func DecodeValue(raw json.RawMessage) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("setting value must be JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("setting value must contain one JSON value")
	}
	return value, nil
}
func (r *Registry) ParseRepositoryValue(namespace, key, text string) (json.RawMessage, error) {
	o, err := r.repositoryOption(namespace, key)
	if err != nil {
		return nil, err
	}
	var value any
	switch o.Type {
	case "string":
		value = text
	case "bool":
		if text != "true" && text != "false" {
			return nil, fmt.Errorf("expected true or false")
		}
		value = text == "true"
	case "integer":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected an integer")
		}
		value = n
	case "strings":
		value, err = DecodeValue(json.RawMessage(text))
		if err != nil {
			return nil, fmt.Errorf("expected a JSON array of strings")
		}
	default:
		return nil, fmt.Errorf("unsupported registered setting type")
	}
	if err := validate(o, value); err != nil {
		return nil, fmt.Errorf("setting %s.%s: %w", namespace, key, err)
	}
	return json.Marshal(value)
}

// ResolveRepository validates only namespaces whose code is present. It never
// activates unknown settings, but reports them so operators can inspect/remove them.
func (r *Registry) ResolveRepository(global map[string]map[string]any, stored map[string]map[string]json.RawMessage) (map[string]map[string]any, []string, error) {
	overrides := map[string]map[string]any{}
	warnings := []string{}
	for namespace, entries := range stored {
		name := strings.TrimPrefix(namespace, "extensions.")
		available := false
		if r != nil {
			_, available = r.definitions[name]
		}
		if !available || namespace != "extensions."+name {
			warnings = append(warnings, "preserved inactive settings for unavailable namespace "+namespace)
			continue
		}
		overrides[name] = map[string]any{}
		for key, raw := range entries {
			value, err := DecodeValue(raw)
			if err != nil {
				return nil, warnings, err
			}
			if err := r.ValidateRepositorySetting(namespace, key, value); err != nil {
				return nil, warnings, err
			}
			overrides[name][key] = value
		}
	}
	sort.Strings(warnings)
	return r.Resolve(global, overrides), warnings, nil
}
func (r *Registry) RedactRepository(stored map[string]map[string]json.RawMessage) map[string]map[string]json.RawMessage {
	result := map[string]map[string]json.RawMessage{}
	for namespace, entries := range stored {
		result[namespace] = map[string]json.RawMessage{}
		for key, raw := range entries {
			o, err := r.repositoryOption(namespace, key)
			if err != nil || o.Secret {
				result[namespace][key] = json.RawMessage(`"[redacted]"`)
			} else {
				result[namespace][key] = append(json.RawMessage(nil), raw...)
			}
		}
	}
	return result
}
