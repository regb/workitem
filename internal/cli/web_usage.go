package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/usage"
	webaccess "github.com/regb/workitem/internal/web"
	"strings"
)

// Usage runs the usage plugin, as wi usage --json does, and returns its
// results; it reports a plugin that is not enabled instead of failing.
func (b webBackend) Usage(ctx context.Context, refresh bool) (webaccess.Usage, error) {
	result := webaccess.Usage{Results: []usage.QuotaResult{}}
	host := b.cfg.Plugins
	if host == nil {
		return result, nil
	}
	specs, err := host.Specs(ctx, "")
	if err != nil {
		return result, err
	}
	for _, spec := range specs {
		if spec.Name != "usage" || !spec.Enabled {
			continue
		}
		result.Enabled = true
		args := []string{"--json", "show"}
		if refresh {
			args = append(args, "--refresh")
		}
		env := []string{}
		for key, value := range b.cfg.Env {
			if !strings.HasPrefix(key, "WI_PLUGIN") && key != "WI_EXECUTABLE" {
				env = append(env, key+"="+value)
			}
		}
		env = append(env, plugin.Variables(spec, host.SelfPath)...)
		var out, errs bytes.Buffer
		if err := plugin.RunCommand(ctx, spec, args, env, "", nil, &out, &errs); err != nil {
			return result, fmt.Errorf("the usage plugin failed: %s", strings.TrimSpace(errs.String()))
		}
		var snapshot usage.Snapshot
		if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
			return result, fmt.Errorf("the usage plugin returned unreadable results")
		}
		result.Results = append(result.Results, snapshot.Results...)
	}
	return result, nil
}
