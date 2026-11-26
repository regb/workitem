package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ResolutionCompleted  = "completed"
	ResolutionAbandoned  = "abandoned"
	ResolutionSuperseded = "superseded"

	BlockerTime     = "time"
	BlockerItem     = "item"
	BlockerExternal = "external"
	// BlockerUsage waits for a provider's usage limit to reset. The daemon
	// sets it when an agent stops on a usage limit and clears it once a usage
	// check shows the provider allows work again.
	BlockerUsage = "usage"

	// Pre-schema-6 lifecycle names, read only by the one-time database upgrade.
	LegacyStateWorking  = "working"
	LegacyStateWaiting  = "waiting"
	LegacyStateArchived = "archived"

	MaxBlockerEventLength = 200
	MaxBlockerLabelLength = 40
	MaxBlockerMetaKeys    = 16
	MaxBlockerMetaValue   = 256
	MaxBlockerProvider    = 64
	// MaxFollowUpLength bounds a blocker's follow-up message.
	MaxFollowUpLength = 16 * 1024
)

var blockerMetaKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// Blocker is durable operator intent: the item cannot make progress until it
// is cleared. Time and item blockers are cleared by the coordinator when their
// condition is met; usage blockers by the daemon once quota is back; external
// blockers are cleared explicitly or by automation. Each kind is
// self-describing: a time, another item, a provider's usage limit, or the
// name of the external event. Meta is opaque structured data that automation
// can match before clearing the blocker through the same API.
//
// FollowUp is a message for the item's agent: when the blocker is cleared,
// it is sent once the item no longer waits.
type Blocker struct {
	ID     string     `json:"id"`
	Kind   string     `json:"kind"`
	Until  *time.Time `json:"until,omitempty"`
	ItemID string     `json:"item_id,omitempty"`
	Event  string     `json:"event,omitempty"`
	// Label is an optional short display form chosen by whoever creates the
	// blocker, such as "#1234" for a pull request.
	Label string            `json:"label,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
	// Provider names the usage provider a usage blocker waits on, such as
	// claude or codex; Until is then the expected reset, if known.
	Provider  string    `json:"provider,omitempty"`
	FollowUp  string    `json:"follow_up,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// SetBy is who set it, as the daemon verified: its follow-up is sent as
	// them.
	SetBy string `json:"set_by,omitempty"`
}

// FollowUp is a message armed by a cleared blocker. It is sent to the agent
// once the item no longer waits, unless the item was prompted after the
// blocker was set: then the operator has moved on and it is dropped.
type FollowUp struct {
	// ID is the blocker's ID.
	ID      string `json:"id"`
	Message string `json:"message"`
	// BlockedAt is when the blocker was set; ClearedAt when it was cleared.
	BlockedAt time.Time `json:"blocked_at"`
	ClearedAt time.Time `json:"cleared_at"`
	// SetBy is who set the blocker; the message is theirs.
	SetBy string `json:"set_by,omitempty"`
}

// Open reports whether the blocker still prevents progress at now. An expired
// time blocker stops counting immediately, before the coordinator records its
// expiration.
func (b Blocker) Open(now time.Time) bool {
	if b.Kind == BlockerTime {
		return b.Until != nil && b.Until.After(now)
	}
	return true
}

// Describe renders what the blocker waits for.
func (b Blocker) Describe() string {
	switch {
	case b.Kind == BlockerTime && b.Until != nil:
		return "until " + b.Until.Local().Format("2006-01-02 15:04")
	case b.Kind == BlockerItem:
		return "on " + b.ItemID
	case b.Kind == BlockerUsage:
		text := "usage limit"
		if b.Provider != "" {
			text = b.Provider + " " + text
		}
		if b.Until != nil {
			text += ", resets " + b.Until.Local().Format("2006-01-02 15:04")
		}
		return text
	case b.Event != "":
		return b.Event
	default:
		return b.Kind
	}
}

// Short renders the blocker for lists: its label if set, otherwise a compact
// form of its condition. itemName names other items, for example by slug.
func (b Blocker) Short(now time.Time, itemName func(string) string) string {
	if b.Label != "" {
		return b.Label
	}
	switch {
	case b.Kind == BlockerTime && b.Until != nil:
		return "until " + shortTime(*b.Until, now)
	case b.Kind == BlockerUsage:
		if b.Until != nil && b.Until.After(now) {
			return "usage, resets " + shortTime(*b.Until, now)
		}
		return "usage limit"
	case b.Kind == BlockerItem:
		if itemName != nil {
			if name := itemName(b.ItemID); name != "" {
				return "on " + name
			}
		}
		return "on " + b.ItemID
	}
	return b.Describe()
}

// shortTime renders a time near now compactly.
func shortTime(at, now time.Time) string {
	at = at.In(now.Location())
	switch {
	case at.Year() == now.Year() && at.YearDay() == now.YearDay():
		return at.Format("15:04")
	case at.Sub(now) < 6*24*time.Hour:
		return at.Format("Mon 15:04")
	default:
		return at.Format("Jan 2")
	}
}

// ShortList renders several blockers for one item.
func ShortList(blockers []Blocker, now time.Time, itemName func(string) string) string {
	parts := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		parts = append(parts, blocker.Short(now, itemName))
	}
	return strings.Join(parts, ", ")
}

// MetaPairs renders metadata as sorted key=value pairs.
func (b Blocker) MetaPairs() []string {
	keys := make([]string, 0, len(b.Meta))
	for key := range b.Meta {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+b.Meta[key])
	}
	return pairs
}

// ParseBlockerMeta parses repeated key=value arguments.
func ParseBlockerMeta(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	meta := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if !ok {
			return nil, fmt.Errorf("blocker metadata %q must be key=value", value)
		}
		if _, exists := meta[key]; exists {
			return nil, fmt.Errorf("blocker metadata key %q is repeated", key)
		}
		meta[key] = val
	}
	return meta, nil
}

// OpenBlockers returns the blockers that still prevent progress at now.
func (m Manifest) OpenBlockers(now time.Time) []Blocker {
	open := []Blocker{}
	for _, blocker := range m.Blockers {
		if blocker.Open(now) {
			open = append(open, blocker)
		}
	}
	return open
}

// Waiting reports whether the item has an open blocker at now.
func (m Manifest) Waiting(now time.Time) bool {
	return len(m.OpenBlockers(now)) > 0
}

func ValidResolution(resolution string) bool {
	switch resolution {
	case ResolutionCompleted, ResolutionAbandoned, ResolutionSuperseded:
		return true
	}
	return false
}

func ValidBlockerKind(kind string) bool {
	return kind == BlockerTime || kind == BlockerItem || kind == BlockerExternal || kind == BlockerUsage
}

// ValidateBlocker checks a blocker's shape independent of other items.
func ValidateBlocker(b Blocker, itemID string, now time.Time) error {
	if !ValidBlockerKind(b.Kind) {
		return fmt.Errorf("invalid blocker kind %q; expected time, item, external, or usage", b.Kind)
	}
	if len(b.FollowUp) > MaxFollowUpLength || !utf8.ValidString(b.FollowUp) || strings.ContainsRune(b.FollowUp, 0) {
		return fmt.Errorf("blocker follow-up must be valid text of up to %d bytes", MaxFollowUpLength)
	}
	if b.Kind != BlockerUsage && b.Provider != "" {
		return fmt.Errorf("only usage blockers name a provider")
	}
	if len(b.Event) > MaxBlockerEventLength || strings.IndexFunc(b.Event, unicode.IsControl) >= 0 {
		return fmt.Errorf("blocker event must be a single line of up to %d bytes", MaxBlockerEventLength)
	}
	if b.Kind != BlockerExternal && b.Event != "" {
		return fmt.Errorf("only external blockers name an event")
	}
	if utf8.RuneCountInString(b.Label) > MaxBlockerLabelLength || strings.IndexFunc(b.Label, unicode.IsControl) >= 0 {
		return fmt.Errorf("blocker label must be a single line of up to %d characters", MaxBlockerLabelLength)
	}
	if len(b.Meta) > MaxBlockerMetaKeys {
		return fmt.Errorf("blocker metadata allows at most %d keys", MaxBlockerMetaKeys)
	}
	for key, value := range b.Meta {
		if !blockerMetaKey.MatchString(key) {
			return fmt.Errorf("blocker metadata key %q must be lowercase letters, digits, '.', '_', or '-', up to 64 bytes", key)
		}
		if len(value) > MaxBlockerMetaValue || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("blocker metadata %q must be a single line of up to %d bytes", key, MaxBlockerMetaValue)
		}
	}
	switch b.Kind {
	case BlockerTime:
		if b.Until == nil || !b.Until.After(now) {
			return fmt.Errorf("time blocker requires a future time")
		}
		if b.ItemID != "" {
			return fmt.Errorf("time blocker must not reference an item")
		}
	case BlockerItem:
		if !ValidID(b.ItemID) {
			return fmt.Errorf("item blocker requires a valid work item id")
		}
		if b.ItemID == itemID {
			return fmt.Errorf("an item cannot block itself")
		}
		if b.Until != nil {
			return fmt.Errorf("item blocker must not have a time")
		}
	case BlockerUsage:
		if len(b.Provider) > MaxBlockerProvider || strings.IndexFunc(b.Provider, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
			return fmt.Errorf("usage blocker provider must be a single word of up to %d bytes", MaxBlockerProvider)
		}
		if b.ItemID != "" {
			return fmt.Errorf("usage blocker must not reference an item")
		}
	case BlockerExternal:
		if strings.TrimSpace(b.Event) == "" {
			return fmt.Errorf("external blocker requires an event name")
		}
		if b.Until != nil || b.ItemID != "" {
			return fmt.Errorf("external blocker must not have a time or item")
		}
	}
	return nil
}
