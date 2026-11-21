package coordinator

import (
	"encoding/json"
	"slices"
	"time"
)

// PromptLinksProjection keeps, per item, which native history entries each
// prompt a client sent became, by the sender's request ID. History marks
// its messages with them, so a client confirms what it sent by ID, also
// after the agent stopped and its live content is gone.
const PromptLinksProjection = "prompt_links"

// maxPromptLinks bounds an item's links; the oldest go first.
const maxPromptLinks = 500

// PromptLink is one prompt and the native entries it became.
type PromptLink struct {
	RequestID string    `json:"request_id"`
	NativeIDs []string  `json:"native_ids"`
	At        time.Time `json:"at"`
}

type promptLinks struct {
	Links []PromptLink `json:"links"`
}

// validPromptLink requires a request ID and one to 16 bounded native IDs.
func validPromptLink(requestID string, nativeIDs []string) bool {
	if requestID == "" || len(requestID) > 200 || len(nativeIDs) == 0 || len(nativeIDs) > 16 {
		return false
	}
	for _, id := range nativeIDs {
		if id == "" || len(id) > 200 {
			return false
		}
	}
	return true
}

// promptLinkUpdate records a prompt.saved event's link.
func promptLinkUpdate(d *Database, event RuntimeSemanticEvent) ([]ProjectionUpdate, error) {
	var current promptLinks
	if _, err := d.ReadProjection(PromptLinksProjection, event.WorkItemID, &current); err != nil {
		return nil, err
	}
	for _, link := range current.Links {
		if link.RequestID == event.RequestID && slices.Equal(link.NativeIDs, event.NativeIDs) {
			return nil, nil
		}
	}
	current.Links = append(current.Links, PromptLink{RequestID: event.RequestID, NativeIDs: append([]string(nil), event.NativeIDs...), At: event.Timestamp.UTC()})
	if extra := len(current.Links) - maxPromptLinks; extra > 0 {
		current.Links = current.Links[extra:]
	}
	value, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	return []ProjectionUpdate{{Projection: PromptLinksProjection, Key: event.WorkItemID, Value: value}}, nil
}

// PromptRequests maps each native history entry of an item that a linked
// prompt became to the request ID it was sent with.
func (d *Database) PromptRequests(itemID string) (map[string]string, error) {
	var current promptLinks
	if _, err := d.ReadProjection(PromptLinksProjection, itemID, &current); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(current.Links))
	for _, link := range current.Links {
		for _, id := range link.NativeIDs {
			out[id] = link.RequestID
		}
	}
	return out, nil
}
