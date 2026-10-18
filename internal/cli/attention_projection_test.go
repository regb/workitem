package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/testutil"
)

type noAttentionHistoryStore struct {
	app.Store
	t *testing.T
}

func (s noAttentionHistoryStore) ReadEvents(string) ([]model.Event, error) {
	s.t.Error("attention command fetched event history")
	return nil, fmt.Errorf("history must not be read")
}

func TestAttentionJSONUsesCommittedProjection(t *testing.T) {
	stdout, stderr, a := configuredApp(t)
	ctx := context.Background()
	created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Projected JSON", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	opts := app.ResolveOptions{Selector: created.Manifest.ID}
	if _, err := a.SetWorkItemState(ctx, opts, model.StateActive, false); err != nil {
		t.Fatal(err)
	}
	a.Store = noAttentionHistoryStore{Store: a.Store, t: t}
	a.ConversationProjectionObserver = func(context.Context, model.Manifest, time.Time) (app.ConversationStatus, []string) {
		t.Error("attention command inspected conversation")
		return app.ConversationStatus{}, nil
	}
	cfg := cli.Config{App: a, Stdout: stdout, Stderr: stderr, CWD: t.TempDir(), Env: map[string]string{}}
	base := testutil.Time()
	var readAt, unreadAt, deferAt time.Time
	for index, command := range []string{"unread", "read", "defer", "activity"} {
		now := base.Add(time.Duration(index) * time.Minute)
		a.Clock = fixedClock{now}
		stdout.Reset()
		stderr.Reset()
		if code := cli.Run(ctx, []string{"attention", command, "--item", created.Manifest.ID, "--json"}, cfg); code != cli.ExitOK {
			t.Fatalf("%s: %d %s", command, code, stderr.String())
		}
		var result struct {
			WorkItemID string                `json:"work_item_id"`
			Unread     bool                  `json:"unread"`
			MarkedAt   time.Time             `json:"marked_at"`
			Activity   app.AttentionActivity `json:"activity"`
			Warnings   []string              `json:"warnings"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.WorkItemID != created.Manifest.ID || len(result.Warnings) != 0 {
			t.Fatalf("unexpected result: %s", stdout.String())
		}
		switch command {
		case "unread":
			unreadAt = now
			if !result.Unread || !result.MarkedAt.Equal(now) {
				t.Fatal("changed unread JSON result")
			}
		case "read":
			readAt = now
			if result.Unread || !result.MarkedAt.Equal(now) {
				t.Fatal("changed read JSON result")
			}
		case "defer":
			deferAt = now
		}
		if !unreadAt.IsZero() && (result.Activity.LastUnreadAt == nil || !result.Activity.LastUnreadAt.Equal(unreadAt)) {
			t.Fatal("projection omitted unread marker")
		}
		if !readAt.IsZero() && (result.Activity.LastReadAt == nil || !result.Activity.LastReadAt.Equal(readAt)) {
			t.Fatal("projection omitted committed read marker")
		}
		if !deferAt.IsZero() && (result.Activity.LastDeferredAt == nil || !result.Activity.LastDeferredAt.Equal(deferAt)) {
			t.Fatal("projection omitted committed defer marker")
		}
		if strings.Contains(stdout.String(), "unavailable") {
			t.Fatal("attention command silently fell back")
		}
	}
}
