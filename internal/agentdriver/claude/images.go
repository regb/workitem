package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type imageTUIInput interface {
	SendImages(context.Context, string, []protocol.ImageContent, bool) (string, func(), error)
}

func supportsImageInput(input tuiInput) bool {
	_, ok := input.(imageTUIInput)
	return ok && supportsControl(input, protocol.CommandPrompt)
}
func validateImages(images []protocol.ImageContent) error {
	if err := (protocol.ControlCommand{Type: protocol.CommandPrompt, Images: images}).ValidateContent(); err != nil {
		return err
	}
	for _, content := range images {
		format := ""
		switch content.MIMEType {
		case "image/png":
			format = "png"
		case "image/jpeg":
			format = "jpeg"
		default:
			return fmt.Errorf("Claude image controls currently support PNG and JPEG only")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(content.Data)
		if err != nil {
			return err
		}
		config, actual, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || actual != format {
			return fmt.Errorf("invalid or mismatched Claude image format")
		}
		if config.Width < 1 || config.Height < 1 || config.Width > 10000 || config.Height > 10000 || int64(config.Width)*int64(config.Height) > 20000000 {
			return fmt.Errorf("Claude image dimensions exceed bounds")
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("malformed Claude image")
		}
	}
	return nil
}
func stageImages(images []protocol.ImageContent) ([]string, func(), error) {
	if err := validateImages(images); err != nil {
		return nil, func() {}, err
	}
	dir, err := os.MkdirTemp("", "wi-claude-images-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	paths := []string{}
	for i, content := range images {
		extension := ".png"
		if content.MIMEType == "image/jpeg" {
			extension = ".jpg"
		}
		path := filepath.Join(dir, fmt.Sprintf("image-%d%s", i+1, extension))
		data, _ := base64.StdEncoding.Strict().DecodeString(content.Data)
		if err := os.WriteFile(path, data, 0600); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		paths = append(paths, path)
	}
	return paths, cleanup, nil
}

var imageTags = regexp.MustCompile(`^(?:\[Image #[1-9][0-9]*\])+$`)

func (t *tmuxInput) waitImageTag(ctx context.Context, prefix string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, editorWaitTimeout)
	defer cancel()
	for {
		text, err := t.readEditor(waitCtx)
		if err == nil && strings.HasPrefix(text, prefix) {
			newTag := strings.TrimSpace(strings.TrimPrefix(text, prefix))
			if imageTags.MatchString(newTag) && strings.Count(newTag, "[Image #") == 1 {
				return text, nil
			}
		}
		if err != nil {
			if _, ok := err.(editorNotVisible); !ok {
				return "", err
			}
		}
		if err := waitEditorPoll(waitCtx); err != nil {
			return "", fmt.Errorf("Claude image attachment was not confirmed by pane readback")
		}
	}
}

// Native bracketed paste recognizes a local image pathname and reads its bytes
// into the pending image attachment. No system clipboard or model file tool is
// involved. The caller owns cleanup until confirmation or runtime exit.
func (t *tmuxInput) SendImages(ctx context.Context, message string, images []protocol.ImageContent, steer bool) (string, func(), error) {
	paths, cleanup, err := stageImages(images)
	if err != nil {
		return "", func() {}, beforeInputError{err}
	}
	if err := t.waitEmptyEditor(ctx); err != nil {
		cleanup()
		return "", func() {}, beforeInputError{err}
	}
	prefix := ""
	touched := false
	for _, path := range paths {
		buffer := fmt.Sprintf("wi-claude-image-%d-%d", os.Getpid(), time.Now().UnixNano())
		_, err := t.run(ctx, strings.NewReader(path), "load-buffer", "-b", buffer, "-")
		if err == nil {
			if prefix == "" {
				err = t.verify(ctx, true)
			} else {
				err = t.waitPastedDraft(ctx, prefix)
			}
		}
		if err == nil {
			touched = true
			_, err = t.run(ctx, nil, "paste-buffer", "-p", "-b", buffer, "-t", t.pane)
		}
		_, _ = t.run(context.Background(), nil, "delete-buffer", "-b", buffer)
		if err != nil {
			if !touched {
				cleanup()
				return "", func() {}, beforeInputError{err}
			}
			return "", cleanup, inputFailure("paste_uncertain", fmt.Errorf("Claude image paste delivery uncertain; do not resend: %w", err))
		}
		prefix, err = t.waitImageTag(ctx, prefix)
		if err != nil {
			return "", cleanup, inputFailure("paste_readback_missing", fmt.Errorf("Claude image attachment delivery uncertain; no submission shortcut sent; do not resend: %w", err))
		}
	}
	return promptDigest(prefix + message), cleanup, t.sendDraft(ctx, message, steer, prefix)
}
