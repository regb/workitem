package daemonapi

import (
	"encoding/base64"
	"strings"
	"unicode"
	"unicode/utf8"

	p "github.com/regb/workitem/agentdriver/protocol"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func validateMessage(r *v1.SendMessageRequest) error {
	if r.StartMode < 0 || r.StartMode > 2 {
		return invalid("invalid start_mode")
	}
	if r.Delivery < 0 || r.Delivery > 3 {
		return invalid("invalid message delivery")
	}
	if len(r.ClientMessageId) > 200 || strings.TrimSpace(r.ClientMessageId) != r.ClientMessageId || strings.IndexFunc(r.ClientMessageId, unicode.IsControl) >= 0 {
		return invalid("invalid client_message_id")
	}
	command := p.ControlCommand{Type: "prompt", Message: r.Text}
	for _, image := range r.Images {
		command.Images = append(command.Images, p.ImageContent{MIMEType: image.MimeType, Data: base64.StdEncoding.EncodeToString(image.Data)})
	}
	if err := command.ValidateContent(); err != nil {
		return invalid(err.Error())
	}
	return nil
}
func validateStart(start *v1.StartOptions) error {
	if start == nil {
		return nil
	}
	if start.Mode < 0 || start.Mode > 2 {
		return invalid("invalid agent mode")
	}
	if start.SendNothing && start.Prompt != "" {
		return invalid("send_nothing conflicts with prompt")
	}
	if len(start.Prompt) > p.MaxControlMessageBytes || !utf8.ValidString(start.Prompt) {
		return invalid("prompt must be UTF-8 and at most 64 KiB")
	}
	return validateMessage(&v1.SendMessageRequest{ClientMessageId: start.ClientMessageId})
}
