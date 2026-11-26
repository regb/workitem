package app

import (
	viewapp "github.com/regb/workitem/internal/app/view"
)

type WorkListOptions = viewapp.Options
type WorkListItem = viewapp.Item
type WorkAgentStatus = viewapp.AgentStatus
type WorkListResult = viewapp.Result

func (a *App) DeepWorkCapacity() DeepWorkCapacity {
	return a.itemService().DeepWorkCapacity(a.DeepWorkConfig.MaxActive)
}

func (a *App) WorkList(opts WorkListOptions) WorkListResult {
	return a.viewService().WorkList(opts, a.DeepWorkConfig.MaxActive, a.ListConfig.RepositoryFolders)
}
