package pi

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

// The TUI bridge streams content in TypeScript; Go subscribers apply its events
// with driverprotocol.ApplyContent and must reach the bridge's own snapshot.
func TestTUIBridgeStreamMatchesItsSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The script starts the bridge, then runs one step per line on stdin.
	cmd, root := bridgeCommand(t, ctx, "bridge_stream.mjs", "tui")
	control := filepath.Join(root, "control.sock")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := bufio.NewScanner(stdout)
	expect := func(want string) {
		t.Helper()
		for lines.Scan() {
			if strings.TrimSpace(lines.Text()) == want {
				return
			}
		}
		t.Fatalf("bridge never printed %q", want)
	}
	expect("ready")
	path := driverprotocol.ContentSocketPath(control)
	values := make(chan driverprotocol.Content, 256)
	go func() {
		var value driverprotocol.Content
		_ = driverprotocol.SubscribeContent(ctx, path, "owner", "item", func(event driverprotocol.ContentEvent) error {
			value = driverprotocol.ApplyContent(value, event)
			values <- value
			return nil
		})
	}()
	// check runs one step and compares the streamed state with a fresh read.
	check := func(step string) driverprotocol.Content {
		t.Helper()
		fmt.Fprintln(stdin, step)
		expect("done " + step)
		want, err := driverprotocol.ReadContent(ctx, path, "owner", "item")
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.After(3 * time.Second)
		for {
			select {
			case got := <-values:
				if got.Sequence == want.Sequence {
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("after %s streamed %+v\nsnapshot %+v", step, got, want)
					}
					return got
				}
			case <-deadline:
				t.Fatalf("after %s the stream never reached sequence %d", step, want.Sequence)
			}
		}
	}
	got := check("write")
	if got.Text() != "Hello 🌍" || got.Activity != "Tool completed: bash" || got.LeafID != "leaf-1" {
		t.Fatalf("while writing: %+v", got)
	}
	// Parts keep the written order; parallel calls are both kept, each
	// with its own state, in the place it started.
	parts := got.Parts
	if len(parts) != 5 || parts[0].Text != "Hello " || parts[1].Thought == nil || parts[1].Thought.Text != "pondering" || parts[4].Text != "🌍" {
		t.Fatalf("parts: %+v", parts)
	}
	if first, second := parts[2].Tool, parts[3].Tool; first == nil || second == nil || first.ID != "t1" || first.Status != "completed" || !strings.Contains(first.Output, `"text": "a"`) || second.ID != "t2" || second.Status != "running" {
		t.Fatalf("tools: %+v", driverprotocol.PartsTools(parts))
	}
	// The saved message keeps its text, now linked to its session entry.
	if got := check("save"); got.Text() != "Hello 🌍" || got.Activity != "Idle" || got.LeafID != "leaf-2" || !reflect.DeepEqual(got.SavedAs, []string{"entry-9"}) {
		t.Fatalf("after saving: %+v", got)
	}
	fmt.Fprintln(stdin, "stop")
}
