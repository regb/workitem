package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// TestUsageLimitWaitsAndContinuesThroughAFollowUp drives both halves: a
// blocker's follow-up is sent once the item no longer waits, and an agent
// stopped on a usage limit gets a usage blocker whose follow-up continues it.
func TestUsageLimitWaitsAndContinuesThroughAFollowUp(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	var started v1.CreateAndStartItemResponse
	f.json(&started, "new", "--repo", repo.Repository.Id, "--title", "Follow-ups", "--start", "--agent-mode", "headless", "--prompt", "first prompt")
	item := started.Item.Id
	f.historyReady(item, 2)
	f.idle(item)

	getItem := func() *v1.Item {
		t.Helper()
		value, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Item
	}
	lastPrompt := func() string {
		t.Helper()
		value, err := f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		text, at := "", time.Time{}
		for _, message := range value.Msg.Messages {
			if message.Role == v1.MessageRole_MESSAGE_ROLE_USER && !message.Time.AsTime().Before(at) {
				text, at = messageText(message), message.Time.AsTime()
			}
		}
		return text
	}
	waitFor := func(what string, done func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !done(); time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: item %v, last prompt %q", what, getItem(), lastPrompt())
			}
		}
	}

	// A follow-up waits for every blocker, then is sent once.
	f.run("wait", "--for", "review", "--then", "the review is in", item)
	f.run("wait", "--for", "ci", item)
	ids := []string{}
	for _, b := range getItem().Blockers {
		ids = append(ids, b.Id)
	}
	if len(ids) != 2 || getItem().Blockers[0].FollowUp != "the review is in" {
		t.Fatalf("blockers: %v", getItem().Blockers)
	}
	f.run("blocker", "clear", "--id", ids[0], item)
	if followUps := getItem().FollowUps; len(followUps) != 1 || followUps[0].Message != "the review is in" {
		t.Fatalf("armed follow-ups: %v", followUps)
	}
	time.Sleep(6 * time.Second)
	if lastPrompt() == "the review is in" {
		t.Fatal("the follow-up was sent while the item still waited")
	}
	f.run("blocker", "clear", "--id", ids[1], item)
	waitFor("the follow-up", func() bool { return lastPrompt() == "the review is in" && len(getItem().FollowUps) == 0 })
	f.idle(item)

	// A usage limit makes the item wait, with a follow-up to continue.
	f.run("send", "--item", item, "this turn hits the usage limit")
	var usage *v1.Blocker
	waitFor("a usage blocker", func() bool {
		for _, b := range getItem().Blockers {
			if b.Kind == v1.BlockerKind_BLOCKER_KIND_USAGE {
				usage = b
			}
		}
		return usage != nil
	})
	// No usage plugin reports the fake provider: it is retried in an hour.
	if usage.FollowUp != "Continue where you left off." || usage.Provider != "test" || usage.Until == nil || time.Until(usage.Until.AsTime()) < 50*time.Minute {
		t.Fatalf("usage blocker: %v", usage)
	}
	// The saved notice names whose quota ran out, as the blocker does.
	history, err := f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	if last := history.Msg.Messages[len(history.Msg.Messages)-1]; !last.Error || last.UsageProvider != "test" {
		t.Fatalf("usage notice: %v", last)
	}
	list := string(f.run("list"))
	if !strings.Contains(list, "usage, resets") {
		t.Fatalf("list does not show the usage wait:\n%s", list)
	}
	// Clearing it continues the conversation; the next turn succeeds.
	f.run("blocker", "clear", "--id", usage.Id, item)
	waitFor("the continue follow-up", func() bool { return lastPrompt() == "Continue where you left off." })
	f.idle(item)
	time.Sleep(6 * time.Second)
	if blockers := getItem().Blockers; len(blockers) != 0 {
		t.Fatalf("the item waits again: %v", blockers)
	}

	// A usage blocker cleared without its follow-up is not set again for
	// the same failure.
	f.run("send", "--item", item, "another usage limit")
	usage = nil
	waitFor("a second usage blocker", func() bool {
		for _, b := range getItem().Blockers {
			if b.Kind == v1.BlockerKind_BLOCKER_KIND_USAGE {
				usage = b
			}
		}
		return usage != nil
	})
	f.run("blocker", "clear", "--all", "--drop-follow-up", item)
	time.Sleep(6 * time.Second)
	if value := getItem(); len(value.Blockers) != 0 || len(value.FollowUps) != 0 || lastPrompt() != "another usage limit" {
		t.Fatalf("after dropping: %v, last prompt %q", value, lastPrompt())
	}
}
