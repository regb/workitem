package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func testPNG(t *testing.T) protocol.ImageContent {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return protocol.ImageContent{MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(data.Bytes())}
}

type fakeImageInput struct {
	fakeTUIInput
	calls, cleaned int
}

func (f *fakeImageInput) SendImages(_ context.Context, message string, images []protocol.ImageContent, steer bool) (string, func(), error) {
	f.calls++
	return promptDigest("[Image #7]" + message), func() { f.cleaned++ }, f.err
}
func TestImageCommandConfirmationDeduplicationAndCleanup(t *testing.T) {
	s, _, _ := testControls()
	input := &fakeImageInput{}
	s.input = input
	s.ready = true
	c := control("image", protocol.CommandPrompt, "describe")
	c.Images = []protocol.ImageContent{testPNG(t)}
	if err := s.accept(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	s.hook("UserPromptSubmit", promptDigest(c.Message))
	if s.pending == nil || input.cleaned != 0 {
		t.Fatal("confirmed without native image marker")
	}
	s.hook("UserPromptSubmit", promptDigest("[Image #7]"+c.Message))
	if s.pending != nil || input.cleaned != 1 {
		t.Fatal("not confirmed or image file not released")
	}
	if err := s.accept(context.Background(), c); err != nil || input.calls != 1 {
		t.Fatal(err, input.calls)
	}
	c.Images[0].Data = base64.StdEncoding.EncodeToString([]byte("different image"))
	if err := s.accept(context.Background(), c); err == nil {
		t.Fatal("reused command with changed image")
	}
	s.close()
	if input.cleaned != 1 {
		t.Fatal("cleanup repeated")
	}
}
func TestStagedImagesArePrivateValidatedAndRemoved(t *testing.T) {
	img := testPNG(t)
	paths, cleanup, err := stageImages([]protocol.ImageContent{img})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(paths[0])
	dir, _ := os.Stat(filepath.Dir(paths[0]))
	if info.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
		t.Fatal(info.Mode(), dir.Mode())
	}
	cleanup()
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatal("staging retained")
	}
	for _, bad := range []protocol.ImageContent{{MIMEType: "image/jpeg", Data: img.Data}, {MIMEType: "image/png", Data: "not base64"}, {MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("bad png"))}, {MIMEType: "image/webp", Data: img.Data}} {
		if err := validateImages([]protocol.ImageContent{bad}); err == nil {
			t.Fatal("accepted invalid image")
		}
	}
}
func TestImageTransportUsesNativePathPasteAndSingleSubmission(t *testing.T) {
	screen := "───\n❯\n───"
	payload := ""
	paths := []string{}
	pastes, keys := 0, 0
	input := &tmuxInput{pane: "%7", owner: syscall.Getpgrp(), run: func(_ context.Context, reader io.Reader, args ...string) (string, error) {
		if reader != nil {
			data, _ := io.ReadAll(reader)
			payload = string(data)
		}
		switch args[0] {
		case "display-message":
			return fmt.Sprintf("%%7|%d|0|0|0", syscall.Getpgrp()), nil
		case "capture-pane":
			return screen, nil
		case "paste-buffer":
			pastes++
			if strings.HasSuffix(payload, ".png") {
				if _, err := os.Stat(payload); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, payload)
				screen = "───\n❯ [Image #1]\n───"
			} else {
				screen = "───\n❯ [Image #1]" + payload + "\n───"
			}
		case "send-keys":
			keys++
		}
		return "", nil
	}}
	digest, cleanup, err := input.SendImages(context.Background(), "describe", []protocol.ImageContent{testPNG(t)}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if digest != promptDigest("[Image #1]describe") || pastes != 2 || keys != 1 {
		t.Fatal(digest, pastes, keys)
	}
	cleanup()
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatal("image not removed")
	}
}
func TestUncertainImageDeliveryRetainsFilesUntilRuntimeClose(t *testing.T) {
	s, _, _ := testControls()
	input := &fakeImageInput{}
	input.err = inputFailure("paste_readback_missing", fmt.Errorf("uncertain"))
	s.input = input
	s.ready = true
	c := control("image", protocol.CommandPrompt, "")
	c.Images = []protocol.ImageContent{testPNG(t)}
	if err := s.accept(context.Background(), c); err == nil || input.cleaned != 0 {
		t.Fatal("lost uncertain attachment")
	}
	if err := s.accept(context.Background(), c); err == nil || input.calls != 1 {
		t.Fatal("retried uncertain image")
	}
	s.close()
	if input.cleaned != 1 {
		t.Fatal("did not clean up at exit")
	}
}
