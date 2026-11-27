package model

import (
	"fmt"
	"strings"
	"time"
)

// AccessLevel is what an item's agent may do to another item. Each level
// includes the ones before it.
type AccessLevel int

const (
	AccessNone AccessLevel = iota
	// AccessView reads the item: its record, conversation, notes, review
	// and checkout files.
	AccessView
	// AccessMessage also sends its agent messages, and adds notes,
	// attachments, blockers and review comments.
	AccessMessage
	// AccessControl also drives its agent and checkout: control, abort,
	// answers, settings, start and stop, edits and shelving.
	AccessControl
	// AccessIntegrate also captures and approves its revisions and merges
	// it.
	AccessIntegrate
	// AccessManage also closes and reopens it. An item's agent has it on its
	// own item and on the items it delegated.
	AccessManage
)

var accessNames = []string{"none", "view", "message", "control", "integrate", "manage"}

func (l AccessLevel) String() string {
	if l < 0 || int(l) >= len(accessNames) {
		return fmt.Sprintf("level(%d)", int(l))
	}
	return accessNames[l]
}

// ParseAccessLevel reads a level by name.
func ParseAccessLevel(name string) (AccessLevel, error) {
	for i, n := range accessNames[1:] {
		if strings.EqualFold(strings.TrimSpace(name), n) {
			return AccessLevel(i + 1), nil
		}
	}
	return AccessNone, fmt.Errorf("unknown access level %q: use view, message, control, integrate or manage", name)
}

// AnyItem is the subject or object standing for every item.
const (
	EveryItemPrincipal = "item:*"
	EveryItem          = "*"
)

// AccessEntry grants a subject a level on an item, or on every item.
type AccessEntry struct {
	ID string `json:"id"`
	// Subject is who it grants: "item:<id>", or EveryItemPrincipal.
	Subject string `json:"subject"`
	// ItemID is the item it grants on, or EveryItem.
	ItemID    string      `json:"item_id"`
	Level     AccessLevel `json:"level"`
	GrantedBy string      `json:"granted_by"`
	GrantedAt time.Time   `json:"granted_at"`
}

// Validate refuses an entry naming something other than items.
func (e AccessEntry) Validate() error {
	if e.Subject != EveryItemPrincipal && PrincipalItem(e.Subject) == "" {
		return fmt.Errorf("access is granted to an item's agent (item:<id>) or every item's (item:*), not %q", e.Subject)
	}
	if e.ItemID != EveryItem && !ValidID(e.ItemID) {
		return fmt.Errorf("access is granted on an item or on every item (*), not %q", e.ItemID)
	}
	if e.Level <= AccessNone || e.Level > AccessManage {
		return fmt.Errorf("invalid access level %d", e.Level)
	}
	return nil
}

// Matches reports whether the entry covers subject on item.
func (e AccessEntry) Matches(subject, itemID string) bool {
	return (e.Subject == EveryItemPrincipal || e.Subject == subject) && (e.ItemID == EveryItem || e.ItemID == itemID)
}

// ItemAccess is the level an item's agent (subject, "item:<id>") has on an
// item: everything on itself and on what it delegated, directly or through
// items it delegated, otherwise the most its entries grant. Reading an item
// covers the work it delegated: an entry on an item's owner, or on its
// owner's owner, lets the agent read it, and no more. lookup finds items by
// ID to walk the owner chain.
func ItemAccess(subject string, target Manifest, entries []AccessEntry, lookup func(string) (Manifest, bool)) AccessLevel {
	if subject == ItemPrincipal(target.ID) || target.Owner == subject {
		return AccessManage
	}
	var chain []Manifest
	if lookup != nil {
		chain = OwnerChain(target, lookup)
		for _, owner := range chain {
			if ItemPrincipal(owner.ID) == subject {
				return AccessManage
			}
		}
	}
	level := AccessNone
	for _, entry := range entries {
		if entry.Matches(subject, target.ID) && entry.Level > level {
			level = entry.Level
		}
	}
	if level >= AccessView {
		return level
	}
	for _, owner := range chain {
		for _, entry := range entries {
			if entry.Matches(subject, owner.ID) && entry.Level >= AccessView {
				return AccessView
			}
		}
	}
	return level
}
