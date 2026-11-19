package cli

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestReviewRejectsUnusedFlagsBeforeContactingDaemon(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"unknown"}, "unknown review command"},
		{[]string{"checkpoint", "--change", "c1"}, "flag provided but not defined: -change"},
		{[]string{"status", "--capture-id", "once"}, "--capture-id is not supported"},
		{[]string{"diff", "--base", "deadbeef"}, "flag provided but not defined: -base"},
		{[]string{"diff", "--path", "source.go"}, "--path requires --patch"},
		{[]string{"diff", "--patch", "--stat"}, "choose --patch or --stat"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			err := rpcReview(context.Background(), tc.args, Config{Stdout: io.Discard, Stderr: io.Discard}, false)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("got %v, want %s", err, tc.message)
			}
		})
	}
}
