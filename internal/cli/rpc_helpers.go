package cli

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

// newCommandID identifies one control command or request for idempotency.
func newCommandID() string { id, _ := model.NewID(); return id }

func timestamp(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

// apiLabelRules turns label filters into the API's label rules.
func apiLabelRules(rules map[string]bool) []*v1.LabelRule {
	out := []*v1.LabelRule{}
	for label, include := range rules {
		rule := &v1.LabelRule{}
		if include {
			rule.Rule = &v1.LabelRule_Require{Require: label}
		} else {
			rule.Rule = &v1.LabelRule_Exclude{Exclude: label}
		}
		out = append(out, rule)
	}
	return out
}
