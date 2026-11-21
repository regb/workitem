// Package apiview translates API messages into presentation-only view types.
// It does not load storage, inspect processes, or execute application use cases.
package apiview

import (
	"strings"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func Time(value *timestamppb.Timestamp) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.AsTime()
}
func State(value v1.ItemState) string {
	return strings.ToLower(strings.TrimPrefix(value.String(), "ITEM_STATE_"))
}
func Resolution(value v1.Resolution) string {
	if value == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(value.String(), "RESOLUTION_"))
}
func Mode(value v1.AgentMode) string {
	if value == v1.AgentMode_AGENT_MODE_TUI {
		return "tui"
	}
	if value == v1.AgentMode_AGENT_MODE_HEADLESS {
		return "headless"
	}
	return ""
}
func Status(value v1.AgentStatus) string {
	if value == 0 {
		return "unknown"
	}
	return strings.ToLower(strings.TrimPrefix(value.String(), "AGENT_STATUS_"))
}
func Worktree(value v1.WorktreeStatus) string {
	if value == 0 {
		return "unknown"
	}
	return strings.ToLower(strings.TrimPrefix(value.String(), "WORKTREE_STATUS_"))
}
func Blockers(values []*v1.Blocker) []model.Blocker {
	out := []model.Blocker{}
	for _, v := range values {
		b := model.Blocker{ID: v.Id, Kind: strings.ToLower(strings.TrimPrefix(v.Kind.String(), "BLOCKER_KIND_")), ItemID: v.ItemId, Event: v.Event, Label: v.Label, Meta: v.Meta, Provider: v.Provider, FollowUp: v.FollowUp, CreatedAt: Time(v.CreatedAt)}
		if v.Until != nil {
			t := v.Until.AsTime()
			b.Until = &t
		}
		out = append(out, b)
	}
	return out
}
func Attachment(v *v1.Attachment) itemnotes.Attachment {
	if v == nil {
		return itemnotes.Attachment{}
	}
	return itemnotes.Attachment{Name: v.Name, SHA256: v.Sha256, Size: int64(v.Size), Type: v.ContentType, Path: v.Path}
}
