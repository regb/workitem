package web

import (
	"context"
	"github.com/regb/workitem/internal/app"
	"testing"
)

type uncommittedBackend struct {
	fakeBackend
	changes app.UncommittedChanges
}

func (b *uncommittedBackend) Uncommitted(context.Context, string) (app.UncommittedChanges, error) {
	return b.changes, nil
}

func TestUncommittedJSONUsesEmptyArrays(t *testing.T) {
	for _, test := range []struct {
		name    string
		changes app.UncommittedChanges
		want    string
	}{
		{"empty", app.UncommittedChanges{}, `{"files":[],"attached":[]}`},
		{"attachment only", app.UncommittedChanges{Attached: []string{"ACCESS_DESIGN.md"}}, `{"files":[],"attached":["ACCESS_DESIGN.md"]}`},
		{"real changes", app.UncommittedChanges{Files: []string{"changed.ts"}}, `{"files":["changed.ts"],"attached":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := local(t, &uncommittedBackend{changes: test.changes})
			w := request(s, "GET", "/api/items/"+itemID+"/uncommitted", "")
			if w.Code != 200 || w.Body.String() != test.want+"\n" {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
