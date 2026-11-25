package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// A message from another item's agent reaches an agent wrapped in an
// envelope wi writes: who sent it, and how that sender relates to the
// receiving item. The tag carries a random
// marker, so text inside cannot close it or forge another, and any tag-like
// text in the body is neutralized. The receiving agent is told, in its
// system prompt, to trust the envelope and weigh what is inside.

// Relations of a sending item to the receiving one.
const (
	RelationOwner      = "owner"      // the sender owns the receiving item
	RelationDelegation = "delegation" // the receiving item owns the sender
	RelationOther      = "other"
)

// NewEnvelopeMarker makes an envelope's random marker.
func NewEnvelopeMarker() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Envelope describes a wrapped message's sender.
type Envelope struct {
	Marker string
	// From is the sending principal; Title the sending item's title.
	From, Title, Relation string
}

func (e Envelope) tag() string { return "wi-message-" + e.Marker }

// Wrap puts text in the envelope.
func (e Envelope) Wrap(text string) string {
	body := strings.NewReplacer("<wi-message", "<wi_message", "</wi-message", "</wi_message").Replace(text)
	title := strings.NewReplacer(`"`, "'", "\n", " ", "\r", " ").Replace(e.Title)
	return fmt.Sprintf("<%s from=%q title=%q relation=%q>\n%s\n</%s>", e.tag(), e.From, title, e.Relation, body, e.tag())
}

// Unwrap returns the text a message wrapped by this envelope carried, or the
// message unchanged when it is not one.
func (e Envelope) Unwrap(message string) (string, bool) {
	open := "<" + e.tag() + " "
	closing := "\n</" + e.tag() + ">"
	start := strings.Index(message, open)
	end := strings.LastIndex(message, closing)
	if e.Marker == "" || start < 0 || end < start {
		return message, false
	}
	header := strings.Index(message[start:], ">\n")
	if header < 0 || start+header+2 > end {
		return message, false
	}
	return message[:start] + message[start+header+2:end] + message[end+len(closing):], true
}
