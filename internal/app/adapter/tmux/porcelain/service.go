package porcelain

import (
	"context"
	"fmt"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type Resolver func(context.Context, contract.ResolveOptions) (model.Manifest, error)
type Service struct{ resolve Resolver }

func New(r Resolver) *Service { return &Service{resolve: r} }

type Selection struct {
	Index          int
	CurrentInQueue bool
	Wrapped        bool
}

func SelectNext(orderedIDs []string, currentID string) (Selection, error) {
	if len(orderedIDs) == 0 {
		return Selection{}, fmt.Errorf("nothing needs attention")
	}
	selection := Selection{}
	for i, id := range orderedIDs {
		if id != currentID {
			continue
		}
		selection.CurrentInQueue = true
		selection.Index = i + 1
		if selection.Index == len(orderedIDs) {
			selection.Index = 0
			selection.Wrapped = true
		}
		break
	}
	return selection, nil
}

func (s *Service) ValidateSwitchTarget(ctx context.Context, o contract.ResolveOptions) (model.Manifest, error) {
	m, e := s.resolve(ctx, o)
	if e != nil {
		return model.Manifest{}, e
	}
	if m.State == model.StateArchived {
		return model.Manifest{}, fmt.Errorf("work item %s is archived; run `wi state set backlog --item %s` before entering it", m.ID, m.ID)
	}
	if m.State == model.StateBacklog {
		return model.Manifest{}, fmt.Errorf("work item %s is backlog; use `wi start` to start it", m.ID)
	}
	return m, nil
}
