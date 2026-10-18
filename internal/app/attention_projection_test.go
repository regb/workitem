package app_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestAttentionCommandsReadCompactProjectionWithoutHistoryOrConversation(t *testing.T) {
	for _, command := range []string{"read", "unread", "defer", "activity"} {
		for _, unavailable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unavailable=%t", command, unavailable), func(t *testing.T) {
				ctx := context.Background()
				native := store.New(t.TempDir())
				a := newTestApp(native, newFakeGit(t))
				now := testutil.Time()
				a.Clock = fixedClock{now}
				created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Projected attention", CWD: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				opts := app.ResolveOptions{Selector: created.Manifest.ID}
				if _, err := a.SetWorkItemState(ctx, opts, model.StateActive, false); err != nil {
					t.Fatal(err)
				}
				tracked := &navigationStore{Store: native}
				a.Store = tracked
				a.ConversationProjectionObserver = func(context.Context, model.Manifest, time.Time) (app.ConversationStatus, []string) {
					t.Error("attention command inspected native content")
					return app.ConversationStatus{}, nil
				}
				requested, completed, deferred, read, unread := now.Add(-5*time.Minute), now.Add(-4*time.Minute), now.Add(-3*time.Minute), now.Add(-time.Minute), now.Add(-2*time.Minute)
				projected := app.AttentionActivity{LastRequestedAt: &requested, LastCompletedAt: &completed, LastDeferredAt: &deferred, LastReadAt: &read, LastUnreadAt: &unread}
				calls := 0
				a.AttentionActivityReader = func(_ context.Context, id string) (app.AttentionActivity, error) {
					calls++
					if id != created.Manifest.ID {
						t.Fatal("read another item")
					}
					if unavailable {
						return app.AttentionActivity{}, fmt.Errorf("projection offline")
					}
					return projected, nil
				}
				var activity app.AttentionActivity
				var warnings []string
				switch command {
				case "read", "unread":
					mark := a.MarkRead
					if command == "unread" {
						mark = a.MarkUnread
					}
					result, err := mark(ctx, opts)
					if err != nil {
						t.Fatal(err)
					}
					activity, warnings = result.Activity, result.Warnings
					if result.Unread != (command == "unread") || !result.MarkedAt.Equal(now) {
						t.Fatal("changed read marker result", result)
					}
				case "defer":
					result, err := a.RecordAttentionDefer(ctx, opts)
					if err != nil {
						t.Fatal(err)
					}
					activity, warnings = result.Activity, result.Warnings
				case "activity":
					result, err := a.WorkItemActivity(ctx, opts)
					if err != nil {
						t.Fatal(err)
					}
					activity, warnings = result.Activity, result.Warnings
				}
				if calls != 1 || tracked.historyReads != 0 {
					t.Fatalf("query did not use exactly one projection read: %d, history %d", calls, tracked.historyReads)
				}
				expected := projected
				if unavailable {
					expected = app.AttentionActivity{}
					if len(warnings) != 1 || !strings.Contains(warnings[0], "projection offline") {
						t.Fatal("projection failure was hidden", warnings)
					}
				} else if len(warnings) != 0 {
					t.Fatal(warnings)
				}
				switch command {
				case "read":
					expected.LastReadAt = &now
				case "unread":
					expected.LastUnreadAt = &now
				case "defer":
					expected.LastDeferredAt = &now
				}
				if !reflect.DeepEqual(activity, expected) {
					t.Fatalf("activity = %+v, want %+v", activity, expected)
				}
				events, err := native.ReadEvents(created.Manifest.ID)
				if err != nil {
					t.Fatal(err)
				}
				markers := 0
				for _, event := range events {
					if strings.HasPrefix(event.Type, "attention.") {
						markers++
					}
				}
				want := 1
				if command == "activity" {
					want = 0
				}
				if markers != want {
					t.Fatal("lost or duplicated mutation", markers)
				}
			})
		}
	}
}

func TestReadResultDoesNotOverwriteNewerProjectedMarker(t *testing.T) {
	native := store.New(t.TempDir())
	a := newTestApp(native, newFakeGit(t))
	now := testutil.Time()
	a.Clock = fixedClock{now}
	created, err := a.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Concurrent read", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	a.AttentionActivityReader = func(context.Context, string) (app.AttentionActivity, error) {
		return app.AttentionActivity{LastReadAt: &later}, nil
	}
	result, err := a.MarkRead(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !result.MarkedAt.Equal(now) || !result.Activity.LastReadAt.Equal(later) {
		t.Fatal("read result replaced a newer marker", result)
	}
}
