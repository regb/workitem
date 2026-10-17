package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/options"
)

type RepositoryReader func(context.Context, string) (model.RepositorySettings, error)
type RepositoryLister func(context.Context) ([]string, error)

// Host composes every contribution from one catalog and one settings resolver.
// Each Host is an operation snapshot. Jobs fork a fresh snapshot for each run.
type Host struct {
	Registry         *Registry
	Registrations    map[string]Registration
	Global           map[string]map[string]any
	ReadRepository   RepositoryReader
	ListRepositories RepositoryLister
	SelfPath         string
	mu               sync.Mutex
	cache            map[string]map[string]map[string]any
}

func cloneRegistrations(source map[string]Registration) map[string]Registration {
	result := map[string]Registration{}
	for name, r := range source {
		r.Command = append([]string(nil), r.Command...)
		result[name] = r
	}
	return result
}
func NewHost(r *Registry, registrations map[string]Registration, global map[string]map[string]any, self string, read RepositoryReader) *Host {
	copied := map[string]map[string]any{}
	for name, v := range global {
		copied[name] = options.CloneValues(v)
	}
	return &Host{Registry: r, Registrations: cloneRegistrations(registrations), Global: copied, SelfPath: self, ReadRepository: read, cache: map[string]map[string]map[string]any{}}
}
func (h *Host) Fresh() *Host {
	fresh := NewHost(h.Registry, h.Registrations, h.Global, h.SelfPath, h.ReadRepository)
	fresh.ListRepositories = h.ListRepositories
	return fresh
}
func (h *Host) enabled(d Definition) bool { return d.Required || h.Registrations[d.Name].Enabled }
func (h *Host) Settings(ctx context.Context, repo, name string) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	values, exists := h.cache[repo]
	if !exists {
		values = h.Registry.Options().Resolve(h.Global, nil)
		if repo != "" {
			if h.ReadRepository == nil {
				return nil, fmt.Errorf("repository settings source is unavailable")
			}
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			record, err := h.ReadRepository(readCtx, repo)
			cancel()
			if err != nil {
				return nil, err
			}
			if record.RepositoryID != repo {
				return nil, fmt.Errorf("repository settings identity mismatch")
			}
			values, _, err = h.Registry.Options().ResolveRepository(h.Global, record.Namespaces)
			if err != nil {
				return nil, err
			}
		}
		h.cache[repo] = values
	}
	return options.CloneValues(values[name]), nil
}
func (h *Host) Preparation(approve Approver) Pipeline {
	hooks := []Hooks{}
	for _, d := range h.Registry.Definitions() {
		if h.enabled(d) && d.Native != nil {
			hook := d.Native(approve)
			hook.Name = d.Name
			hooks = append(hooks, hook)
		}
	}
	return Pipeline{Hooks: hooks, Options: h.Settings}
}
func (h *Host) Specs(ctx context.Context, repo string) ([]Spec, error) {
	specs := []Spec{}
	if h == nil {
		return specs, nil
	}
	for _, d := range h.Registry.Definitions() {
		settings, err := h.Settings(ctx, repo, d.Name)
		if err != nil {
			return nil, err
		}
		command := append([]string(nil), d.Command...)
		if override := h.Registrations[d.Name].Command; len(override) > 0 {
			command = append([]string(nil), override...)
		}
		specs = append(specs, Spec{Name: d.Name, Enabled: h.enabled(d), Command: command, Settings: settings, Instructions: d.Instructions, Commands: append([]string(nil), d.Commands...), RepositoryID: repo})
	}
	return specs, nil
}
func (h *Host) JobSpecs(ctx context.Context, repo string) ([]Spec, error) {
	specs, err := h.Specs(ctx, repo)
	if err != nil {
		return nil, err
	}
	result := []Spec{}
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		d := h.Registry.definitions[spec.Name]
		for _, job := range d.Jobs {
			value := spec.Settings[job.IntervalOption]
			var seconds int64
			switch n := value.(type) {
			case int:
				seconds = int64(n)
			case int64:
				seconds = n
			case json.Number:
				seconds, _ = n.Int64()
			default:
				return nil, fmt.Errorf("invalid job interval type")
			}
			if seconds < 30 || seconds > 86400 {
				return nil, fmt.Errorf("plugin %s job interval must be between 30 and 86400 seconds", spec.Name)
			}
			copy := spec
			copy.JobName = job.Name
			copy.Interval = time.Duration(seconds) * time.Second
			copy.Command = append(append([]string(nil), spec.Command...), job.Args...)
			result = append(result, copy)
		}
	}
	return result, nil
}
func (h *Host) Instructions(ctx context.Context, repo string) (map[string]string, error) {
	result := map[string]string{}
	if h == nil {
		return result, nil
	}
	for _, d := range h.Registry.Definitions() {
		if h.enabled(d) && d.Instructions != "" {
			if _, err := h.Settings(ctx, repo, d.Name); err != nil {
				return nil, err
			}
			result[d.Name] = d.Instructions
		}
	}
	return result, nil
}
