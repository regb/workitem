package driverprotocol

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxControlMessageBytes = 64 << 10
	MaxControlImages       = 4
	MaxControlImageBytes   = 8 << 20
	// Allow base64 expansion, escaped message text, and envelope metadata.
	MaxControlCommandBytes = 12 << 20
)

// ValidateContent bounds prompt content independently of the access mechanism.
// Drivers must also enforce these limits for clients connecting directly.
func (c ControlCommand) ValidateContent() error {
	if len(c.Message) > MaxControlMessageBytes || !utf8.ValidString(c.Message) {
		return fmt.Errorf("control message must be UTF-8 and at most 64 KiB")
	}
	if len(c.Images) > MaxControlImages {
		return fmt.Errorf("at most four control images are allowed")
	}
	if len(c.Images) > 0 && c.Type != CommandPrompt && c.Type != CommandSteer && c.Type != CommandFollowUp {
		return fmt.Errorf("control %s does not accept images", c.Type)
	}
	total := 0
	for _, image := range c.Images {
		switch image.MIMEType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return fmt.Errorf("unsupported control image type %q", image.MIMEType)
		}
		if len(image.Data) == 0 || strings.ContainsAny(image.Data, "\r\n") || len(image.Data) > base64.StdEncoding.EncodedLen(MaxControlImageBytes) {
			return fmt.Errorf("control image data must be nonempty and at most 8 MiB decoded")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(image.Data)
		if err != nil {
			return fmt.Errorf("invalid control image base64 data")
		}
		total += len(decoded)
		if total > MaxControlImageBytes {
			return fmt.Errorf("control images exceed 8 MiB total")
		}
	}
	return nil
}
