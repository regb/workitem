package pi

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
)

const imageNote = "[Image: original 3679x1008, displayed at 2000x548. Multiply coordinates by 1.84 to map to original image.]"

func TestHistoryHidesPiImageNotesFromPrompts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	data := fmt.Sprintf(`{"type":"session","version":3,"id":"session"}
{"type":"message","id":"only","parentId":null,"message":{"role":"user","content":[{"type":"text","text":"\n\n%[1]s"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]}}
{"type":"message","id":"both","parentId":"only","message":{"role":"user","content":[{"type":"text","text":"look at this\n\n%[1]s"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]}}
{"type":"message","id":"quoted","parentId":"both","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"You wrote %[1]s"}]}}
`, imageNote)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadHistory(context.Background(), path, "")
	if err != nil || len(result.Messages) != 3 {
		t.Fatalf("%+v %v", result, err)
	}
	if only := result.Messages[0]; only.Text() != "" || len(only.Images()) != 1 {
		t.Fatalf("image-only prompt: %+v", only)
	}
	if both := result.Messages[1]; both.Text() != "look at this" {
		t.Fatalf("prompt with text: %q", both.Text())
	}
	// Only prompts lose the note; an answer quoting it is left as written.
	if quoted := result.Messages[2]; quoted.Text() != "You wrote "+imageNote {
		t.Fatalf("answer: %q", quoted.Text())
	}
}

func TestBridgeMatchesPromptsDespitePiImageNotes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node not installed")
	}
	if err := exec.Command(node, "--experimental-strip-types", "--eval", "").Run(); err != nil {
		t.Skip("Node type stripping unavailable")
	}
	root := testutil.ShortTempDir(t)
	bridge := filepath.Join(root, "bridge.ts")
	if err := os.WriteFile(bridge, bridgeSource, 0600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`import { savedPromptText } from %q;
import assert from 'node:assert/strict';
const note = %q;
assert.equal(savedPromptText([{ type: 'text', text: '\n\n' + note }, { type: 'image' }]), '');
assert.equal(savedPromptText([{ type: 'text', text: 'look at this\n\n' + note }, { type: 'image' }]), 'look at this');
assert.equal(savedPromptText([{ type: 'text', text: 'two\n\n' + note + '\n\n' + note }]), 'two');
assert.equal(savedPromptText('plain text'), 'plain text');
assert.equal(savedPromptText([{ type: 'text', text: 'an [Image: reference] I typed' }]), 'an [Image: reference] I typed');
`, (&url.URL{Scheme: "file", Path: bridge}).String(), imageNote)
	scriptPath := filepath.Join(root, "test.mjs")
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "--experimental-strip-types", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("bridge %v\n%s", err, output)
	}
}
