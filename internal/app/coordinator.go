package app

import "github.com/regb/workitem/internal/coordinator"

func (a *App) coordinatorClient() *coordinator.Client {
	if a.Coordinator != nil {
		return a.Coordinator
	}
	return &coordinator.Client{SocketPath: a.DaemonSocketPath}
}
