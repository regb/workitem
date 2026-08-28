package app

import (
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/model"
)

type WorkListOptions = viewapp.Options
type WorkListItem = viewapp.Item
type WorkAgentStatus = viewapp.AgentStatus
type WorkListSections = viewapp.Sections
type WorkListResult = viewapp.Result

func (a *App) DeepWorkCapacity() DeepWorkCapacity {
	return a.itemService().DeepWorkCapacity(a.DeepWorkConfig.MaxActive)
}

func (a *App) WorkList(opts WorkListOptions) WorkListResult {
	return a.viewService().WorkList(opts, a.DeepWorkConfig.MaxActive, a.ListConfig.RepositoryFolders)
}

func (a *App) ProjectWorkListItem(manifest model.Manifest) WorkListItem {
	return viewapp.ProjectItem(manifest, a.ListConfig.RepositoryFolders)
}
