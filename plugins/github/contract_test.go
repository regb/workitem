package github

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestSyncRejectsInvalidListContractsInsteadOfSilentlySkippingWaits(t *testing.T) {
	for _, output := range []string{
		`{"sections":{"waiting":[]}}`,
		`{"items":[{"blockers":[{"kind":"external"}]}]}`,
		`{"items":[{"blockers":[{"created_at":"not a timestamp"}]}]}`,
	} {
		var out bytes.Buffer
		p := Plugin{Wi: "wi", Gh: "gh", Out: &out, Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name != "wi" {
				t.Fatal("invalid list reached GitHub", name)
			}
			return []byte(output), nil
		}}
		if err := p.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "decode wi list") {
			t.Fatalf("%s: %v", output, err)
		}
	}
}

func TestMissingWaitTimestampDoesNotCountHistoricalActivity(t *testing.T) {
	state := State{State: "OPEN", Comments: []Comment{{Author: Author{Login: "reviewer"}, CreatedAt: time.Now()}}}
	if done, _ := Decide(Blocker{}, state, Authors{Self: "waiting-user"}); done {
		t.Fatal("missing wait timestamp made historical activity count as new")
	}
	state.State = "MERGED"
	if done, _ := Decide(Blocker{}, state, Authors{Self: "waiting-user"}); !done {
		t.Fatal("merge does not depend on the wait timestamp")
	}
}
