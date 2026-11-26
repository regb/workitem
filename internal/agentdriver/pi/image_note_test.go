package pi

import (
	"fmt"
	"testing"
)

const imageNote = "[Image: original 3679x1008, displayed at 2000x548. Multiply coordinates by 1.84 to map to original image.]"

func TestHistoryHidesPiImageNotesFromPrompts(t *testing.T) {
	result, err := readHistory(t, fmt.Sprintf(`{"type":"session","version":3,"id":"session"}
{"type":"message","id":"only","parentId":null,"message":{"role":"user","content":[{"type":"text","text":"\n\n%[1]s"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]}}
{"type":"message","id":"both","parentId":"only","message":{"role":"user","content":[{"type":"text","text":"look at this\n\n%[1]s"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]}}
{"type":"message","id":"quoted","parentId":"both","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"You wrote %[1]s"}]}}
`, imageNote))
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
	runBridgeScript(t, "bridge_image_note.mjs", "tui", "PI_IMAGE_NOTE="+imageNote)
}
