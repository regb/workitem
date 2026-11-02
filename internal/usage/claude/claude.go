// Package claude queries Claude Code's zero-turn local usage command.
// Authentication stays inside Claude Code.
package claude

import (
	"context"
	"time"

	"github.com/regb/workitem/internal/usage"
)

type Adapter struct {
	Root        string
	Command     []string
	Environment map[string]string
	Now         func() time.Time
}

func (a *Adapter) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}
func (a *Adapter) Account() usage.Account                { return usage.LocalAccount("claude", a.Root) }
func (a *Adapter) Quota(ctx context.Context) usage.Quota { return a.nativeQuota(ctx) }
