package app

import (
	"context"
	"errors"
	"github.com/regb/workitem/internal/model"
	"testing"
	"time"
)

func TestControllerOperationsSerializeAcrossAppInstances(t *testing.T) {
	a, st, m := newCoreTestApp(t, time.Now(), model.StateActive, false)
	other := New(st, nil)
	unlock, err := a.lockController(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := other.lockController(ctx, m.ID); err == nil {
		release()
		t.Fatal("another App bypassed the controller lock")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
