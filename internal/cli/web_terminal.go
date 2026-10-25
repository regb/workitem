package cli

import (
	"context"
	"fmt"
)

// TerminalClients counts the terminal clients attached to an item's tmux
// session, which closing or shelving it would disconnect.
func (b webBackend) TerminalClients(ctx context.Context, id string) (int, error) {
	m, err := b.cfg.App.ResolveItem(ctx, b.resolve(id))
	if err != nil {
		return 0, err
	}
	// Without a session name tmux would pick a default session.
	session := m.TerminalSessionName()
	if b.cfg.App.Tmux == nil || session == "" {
		return 0, nil
	}
	inspector, ok := b.cfg.App.Tmux.(interface {
		SessionClients(context.Context, string) ([]string, error)
	})
	if !ok {
		return 0, fmt.Errorf("cannot inspect attached terminal clients")
	}
	clients, err := inspector.SessionClients(ctx, session)
	return len(clients), err
}

func (b webBackend) confirmTerminal(ctx context.Context, id string, confirmed bool) error {
	clients, err := b.TerminalClients(ctx, id)
	if err != nil {
		return err
	}
	if clients > 0 && !confirmed {
		return fmt.Errorf("a terminal is attached to this item's tmux session; confirm to close it and disconnect the terminal")
	}
	return nil
}
