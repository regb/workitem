package coordinator

import (
	"strconv"
	"sync"
	"time"
)

// Changes requests wait for item changes instead of polling.
type ChangesRequest struct {
	// Epoch and After are what the caller has seen; an empty epoch or zero
	// revision asks for the current revision without waiting.
	Epoch      string `json:"epoch,omitempty"`
	After      uint64 `json:"after"`
	WaitMillis int    `json:"wait_millis,omitempty"`
}

// ChangesResult names the items changed after the requested revision. All
// means the caller must assume every item changed: it started from zero, or
// fell further behind than the daemon remembers, or the daemon restarted.
type ChangesResult struct {
	// Epoch identifies the daemon instance; revisions restart with it.
	Epoch    string   `json:"epoch"`
	Revision uint64   `json:"revision"`
	ItemIDs  []string `json:"item_ids,omitempty"`
	All      bool     `json:"all,omitempty"`
}

// maxRecentChanges bounds the changes remembered for callers that fall behind.
const maxRecentChanges = 1024

type itemChange struct {
	revision uint64
	itemID   string
}

// changeLog counts structured changes in memory: committed events and
// controller lease handovers. It is a wake-up signal for readers, not a
// record; it starts over when the daemon restarts, which callers notice as
// a revision they have not seen.
type changeLog struct {
	mu       sync.Mutex
	epoch    string
	revision uint64
	recent   []itemChange
	notify   chan struct{}
}

// newChangeLog starts at revision 1, so a caller that has seen nothing (0)
// is never told it is current.
func newChangeLog() *changeLog {
	return &changeLog{epoch: strconv.FormatInt(time.Now().UnixNano(), 36), revision: 1, notify: make(chan struct{})}
}

// eventItems lists the distinct items of committed events.
func eventItems(itemIDs ...string) []string {
	seen := map[string]bool{}
	items := []string{}
	for _, id := range itemIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			items = append(items, id)
		}
	}
	return items
}

// record notes that items changed and wakes waiting readers.
func (l *changeLog) record(itemIDs ...string) {
	if l == nil || len(itemIDs) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.revision++
	for _, id := range itemIDs {
		l.recent = append(l.recent, itemChange{revision: l.revision, itemID: id})
	}
	if len(l.recent) > maxRecentChanges {
		l.recent = append([]itemChange(nil), l.recent[len(l.recent)-maxRecentChanges:]...)
	}
	close(l.notify)
	l.notify = make(chan struct{})
}

// recordAll notes a change that may concern every item, such as a repository
// rename or a blocker sweep.
func (l *changeLog) recordAll() { l.record(allItems) }

// allItems marks a change to every item in the recent changes.
const allItems = ""

// since reports what changed after a revision, and a channel closed on the
// next change.
func (l *changeLog) since(epoch string, after uint64) (ChangesResult, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := ChangesResult{Epoch: l.epoch, Revision: l.revision}
	switch {
	case epoch != l.epoch || after == 0 || after > l.revision:
		// A first request, or a revision from another daemon instance.
		result.All = true
	case after < l.revision:
		if len(l.recent) == 0 || l.recent[0].revision > after+1 {
			result.All = true
			break
		}
		seen := map[string]bool{}
		for _, change := range l.recent {
			if change.revision > after && change.itemID == allItems {
				return ChangesResult{Epoch: l.epoch, Revision: l.revision, All: true}, l.notify
			}
			if change.revision > after && !seen[change.itemID] {
				seen[change.itemID] = true
				result.ItemIDs = append(result.ItemIDs, change.itemID)
			}
		}
	}
	return result, l.notify
}
