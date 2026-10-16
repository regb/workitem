package app

import (
	"context"
	"github.com/regb/workitem/internal/model"
	"testing"
	"time"
)

func TestBlockerChangesHaveNoNativeSaveFallback(t *testing.T) {
	a, st, m := newCoreTestApp(t, time.Now(), model.StateActive, false)
	if _, err := a.AddBlockers(context.Background(), ResolveOptions{Selector: m.ID}, []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}}); err == nil {
		t.Fatal("blockers changed without a command store")
	}
	after, err := st.LoadManifest(m.ID)
	if err != nil || len(after.Blockers) != 0 {
		t.Fatal("failed command changed native state", err)
	}
}
