// Package bundled is the composition root for in-process extensions.
package bundled

import (
	"context"
	"fmt"
	"sync"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/extension"
	direnvext "github.com/regb/workitem/internal/extensions/direnv"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/options"
)

func Registry() *options.Registry {
	r, err := options.New(direnvext.Definition())
	if err != nil {
		panic(err)
	}
	return r
}

type RepositoryReader func(context.Context, string) (model.RepositorySettings, error)

// New freezes effective settings lazily per repository for this CLI operation.
// Only the host reads repository state; hooks receive resolved private copies.
func New(cfg config.Config, approve extension.Approver, read RepositoryReader) extension.Pipeline {
	registry := Registry()
	var mu sync.Mutex
	cache := map[string]map[string]map[string]any{}
	resolve := func(ctx context.Context, id, name string) (map[string]any, error) {
		mu.Lock()
		defer mu.Unlock()
		values, ok := cache[id]
		if !ok {
			if id == "" {
				values = cfg.ExtensionSettings(registry)
			} else {
				if read == nil {
					return nil, fmt.Errorf("repository settings source is unavailable")
				}
				record, err := read(ctx, id)
				if err != nil {
					return nil, err
				}
				if record.RepositoryID != id {
					return nil, fmt.Errorf("repository settings identity mismatch")
				}
				values, _, err = registry.ResolveRepository(cfg.Extensions, record.Namespaces)
				if err != nil {
					return nil, fmt.Errorf("repository extension settings: %w", err)
				}
			}
			cache[id] = values
		}
		return options.CloneValues(values[name]), nil
	}
	return extension.Pipeline{Hooks: []extension.Hooks{direnvext.New(direnv.New("direnv"), approve)}, Options: resolve}
}
