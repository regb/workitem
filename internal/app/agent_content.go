package app

import (
	"context"
	"fmt"
	"path/filepath"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/model"
)

type historyBackend interface {
	History(context.Context, string, string) (driverprotocol.History, error)
}

// ConversationHistory reads the canonical primary conversation through its
// driver. Reading neither materializes a checkout nor starts a runtime.
func (a *App) ConversationHistory(ctx context.Context, opts ResolveOptions, leaf string) (driverprotocol.History, error) {
	return a.conversationHistory(ctx, opts, leaf, "")
}

// ConversationHistoryPage reads an older page pinned to the selected native branch.
func (a *App) ConversationHistoryPage(ctx context.Context, opts ResolveOptions, leaf, before string) (driverprotocol.History, error) {
	return a.conversationHistory(ctx, opts, leaf, before)
}
func (a *App) conversationHistory(ctx context.Context, opts ResolveOptions, leaf, before string) (driverprotocol.History, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return driverprotocol.History{}, err
	}
	if m.PrimaryConversation == nil {
		return driverprotocol.History{Messages: []driverprotocol.HistoryMessage{}}, nil
	}
	c := m.PrimaryConversation
	backend, ok := a.conversationBackend(m, *c).(historyBackend)
	if !ok {
		return driverprotocol.History{}, fmt.Errorf("driver %q does not provide readable history", c.Driver)
	}
	path, err := a.primaryAgentService().AbsPath(m.ID, filepath.FromSlash(c.NativeLocator()))
	if err != nil {
		return driverprotocol.History{}, err
	}
	if before != "" {
		pager, ok := backend.(interface {
			HistoryPage(context.Context, string, string, string) (driverprotocol.History, error)
		})
		if !ok {
			return driverprotocol.History{}, fmt.Errorf("driver %q does not provide history paging", c.Driver)
		}
		return pager.HistoryPage(ctx, path, leaf, before)
	}
	return backend.History(ctx, path, leaf)
}

// contentOwner returns the item and its verified live runtime, whose content
// socket may be read.
func (a *App) contentOwner(ctx context.Context, opts ResolveOptions) (string, *model.AgentRuntime, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return "", nil, err
	}
	r, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return "", nil, err
	}
	if !a.primaryAgentService().ObserveOwnership(r).ControlAvailable {
		return "", nil, fmt.Errorf("no verified live runtime content endpoint")
	}
	if !r.Capabilities.LiveContent {
		return "", nil, fmt.Errorf("agent driver does not provide live content; an existing owner may need an explicit restart after settling")
	}
	return m.ID, r, nil
}

// AgentContentStream follows the verified current owner's content: a snapshot
// event, then every later event, each passed to apply. It returns when the
// stream ends, for example because the runtime stopped; callers subscribe
// again to follow its successor. driverprotocol.ErrContentStreamUnsupported
// means the runtime only answers snapshot reads.
func (a *App) AgentContentStream(ctx context.Context, opts ResolveOptions, apply func(driverprotocol.ContentEvent) error) error {
	id, r, err := a.contentOwner(ctx, opts)
	if err != nil {
		return err
	}
	return driverprotocol.SubscribeContent(ctx, driverprotocol.ContentSocketPath(a.runtimeControlSocketPath(id, r)), r.ID, id, apply)
}

// AgentContent reads a bounded snapshot from the verified current owner.
func (a *App) AgentContent(ctx context.Context, opts ResolveOptions) (driverprotocol.Content, error) {
	id, r, err := a.contentOwner(ctx, opts)
	if err != nil {
		return driverprotocol.Content{}, err
	}
	value, err := driverprotocol.ReadContent(ctx, driverprotocol.ContentSocketPath(a.runtimeControlSocketPath(id, r)), r.ID, id)
	if err != nil {
		return driverprotocol.Content{}, fmt.Errorf("live content unavailable, existing drivers may need an explicit restart after settling: %w", err)
	}
	current, err := a.Store.LoadAgentRuntime(id)
	if err != nil {
		return driverprotocol.Content{}, err
	}
	if current == nil || current.ID != r.ID || !a.primaryAgentService().ObserveOwnership(current).ProcessAlive {
		return driverprotocol.Content{}, fmt.Errorf("runtime owner changed while reading content")
	}
	return value, nil
}
