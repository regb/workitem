package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/regb/workitem/internal/usage"
)

var errNoNativeQuota = errors.New("native Claude returned no plan limits")

// Only the final local-command result can establish that this was a zero-turn
// operation. Do not publish earlier assistant reports until that guard passes.
func decodeLocalUsage(raw []byte) ([]usage.Window, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var report json.RawMessage
	var result struct {
		Type   string   `json:"type"`
		Local  string   `json:"local_command"`
		Turns  *int     `json:"num_turns"`
		APIMS  *int     `json:"duration_api_ms"`
		Cost   *float64 `json:"total_cost_usd"`
		Failed bool     `json:"is_error"`
	}
	results := 0
	for frames := 0; ; frames++ {
		if frames >= 1000 {
			return nil, fmt.Errorf("too many native frames")
		}
		var frame struct {
			Type   string          `json:"type"`
			Report json.RawMessage `json:"usage_report"`
		}
		var object json.RawMessage
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("invalid native usage stream")
		}
		if json.Unmarshal(object, &frame) != nil {
			return nil, fmt.Errorf("invalid native usage frame")
		}
		switch frame.Type {
		case "assistant":
			if len(frame.Report) > 0 {
				report = frame.Report
			}
		case "result":
			results++
			if json.Unmarshal(object, &result) != nil {
				return nil, fmt.Errorf("invalid native usage result")
			}
		}
	}
	if results != 1 || result.Local != "usage" || result.Turns == nil || *result.Turns != 0 || result.APIMS == nil || *result.APIMS != 0 || result.Cost == nil || *result.Cost != 0 || result.Failed {
		return nil, fmt.Errorf("not a verified zero-turn local usage result")
	}
	if len(report) > 0 {
		// A present-but-null report is unknown data, not permission to interpret
		// percentages in unrelated session statistics or recommendations.
		return decodeUsageReport(report)
	}
	return nil, errNoNativeQuota
}

// SDKUsageReport is a documented native assistant-message field. Only plan
// meters are read; the report's session counters and extra-usage spend are ignored.
func decodeUsageReport(raw []byte) ([]usage.Window, error) {
	var report struct {
		RateLimits *struct {
			Limits []struct {
				Kind    string   `json:"kind"`
				Percent *float64 `json:"percent"`
				Reset   *string  `json:"resets_at"`
				Scope   *struct {
					Model *struct {
						Name string `json:"display_name"`
					} `json:"model"`
					Surface *struct {
						Name string `json:"display_name"`
					} `json:"surface"`
				} `json:"scope"`
			} `json:"limits"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &report) != nil {
		return nil, fmt.Errorf("invalid native quota report")
	}
	if report.RateLimits == nil || len(report.RateLimits.Limits) == 0 {
		return nil, errNoNativeQuota
	}
	windows := []usage.Window{}
	counts := map[string]int{}
	for _, row := range report.RateLimits.Limits {
		if !nativeMeterKind.MatchString(row.Kind) {
			continue
		}
		scope := &usage.WindowScope{}
		if row.Scope != nil {
			if row.Scope.Model != nil {
				scope.Model = scopeLabel(row.Scope.Model.Name)
			}
			if row.Scope.Surface != nil {
				scope.Surface = scopeLabel(row.Scope.Surface.Name)
			}
		}
		id := row.Kind
		var duration *int64
		switch row.Kind {
		case "session":
			id = "five_hour"
			v := int64(18000)
			duration = &v
		case "weekly_all":
			id = "seven_day"
			v := int64(604800)
			duration = &v
		case "weekly_scoped":
			id = "seven_day_scoped"
			v := int64(604800)
			duration = &v
			if scope.Surface == "" {
				switch strings.ToLower(scope.Model) {
				case "sonnet":
					id = "seven_day_sonnet"
				case "opus":
					id = "seven_day_opus"
				}
			}
		}
		var reset *time.Time
		if row.Reset != nil {
			if t, err := time.Parse(time.RFC3339, *row.Reset); err == nil && t.Year() >= 1970 {
				v := t.UTC()
				reset = &v
			}
		}
		if w := usage.NativeWindow(id, row.Percent, nil, duration); w != nil {
			w.ResetsAt = reset
			counts[id]++
			if counts[id] > 1 {
				w.ID = fmt.Sprintf("%s_%d", id, counts[id])
			}
			if scope.Model != "" || scope.Surface != "" {
				w.Scope = scope
			}
			windows = append(windows, *w)
		}
	}
	if len(windows) == 0 {
		return nil, errNoNativeQuota
	}
	return windows, nil
}

var nativeMeterKind = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func scopeLabel(s string) string {
	if len(s) > 160 {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return s
}
