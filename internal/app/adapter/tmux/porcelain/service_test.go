package porcelain

import (
	"context"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

func TestValidateSwitchTarget(t *testing.T) {
	for _, tt := range []struct{ name, state, want string }{
		{"working", model.StateWorking, ""}, {"waiting", model.StateWaiting, ""},
		{"backlog", model.StateBacklog, "use `wi start`"}, {"archived", model.StateArchived, "state set backlog"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := New(func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
				return model.Manifest{ID: "item-1", State: tt.state}, nil
			})
			got, err := s.ValidateSwitchTarget(context.Background(), contract.ResolveOptions{})
			if tt.want == "" {
				if err != nil || got.ID != "item-1" {
					t.Fatalf("got=%+v err=%v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
