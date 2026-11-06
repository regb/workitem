package cli

import (
	"errors"
	"fmt"
	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func readDescriptionFileFlag(cfg Config, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) && cfg.CWD != "" {
		path = filepath.Join(cfg.CWD, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read --desc-file: %w", err)
	}
	return string(b), nil
}

func readAgentControlMessage(cfg Config, args []string, filePath string, fromStdin bool) (string, error) {
	sources := 0
	if strings.TrimSpace(filePath) != "" {
		sources++
	}
	if fromStdin {
		sources++
	}
	if len(args) > 0 {
		sources++
	}
	if sources == 0 {
		return "", errors.New("pass a message with --stdin, --file, or positional text")
	}
	if sources > 1 {
		return "", errors.New("pass only one message source: --stdin, --file, or positional text")
	}
	var message string
	if fromStdin {
		value, err := io.ReadAll(cfg.Stdin)
		if err != nil {
			return "", err
		}
		message = string(value)
	} else if strings.TrimSpace(filePath) != "" {
		path := strings.TrimSpace(filePath)
		if !filepath.IsAbs(path) && cfg.CWD != "" {
			path = filepath.Join(cfg.CWD, path)
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		message = string(value)
	} else {
		message = strings.Join(args, " ")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", errors.New("agent control message is empty")
	}
	return message, nil
}

// snoozeHour is when named days (tomorrow, monday, ...) begin.
const snoozeHour = 9

// parseBlockUntil accepts a relative duration (Go syntax plus a d suffix for
// days), "tomorrow" or a weekday name meaning the next one at 09:00 local, or
// an absolute local date or date-time.
func parseBlockUntil(now time.Time, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if at, ok := namedDay(now, strings.ToLower(value)); ok {
		return at, nil
	}
	if strings.HasSuffix(value, "d") {
		if days, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64); err == nil && days > 0 {
			return now.Add(time.Duration(days * float64(24*time.Hour))), nil
		}
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration <= 0 {
			return time.Time{}, fmt.Errorf("--until duration must be positive")
		}
		return now.Add(duration), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if at, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			if !at.After(now) {
				return time.Time{}, fmt.Errorf("--until %s is not in the future", value)
			}
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --until %q; use a duration such as 4h or 2d, tomorrow or a weekday, or a date such as 2026-10-20", value)
}

// namedDay resolves tomorrow or a weekday to the next such day at
// snoozeHour, strictly after now.
func namedDay(now time.Time, value string) (time.Time, bool) {
	local := now.In(time.Local)
	morning := func(days int) time.Time {
		day := local.AddDate(0, 0, days)
		return time.Date(day.Year(), day.Month(), day.Day(), snoozeHour, 0, 0, 0, time.Local)
	}
	if value == "tomorrow" {
		return morning(1), true
	}
	for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
		if value != strings.ToLower(weekday.String()) {
			continue
		}
		days := (int(weekday) - int(local.Weekday()) + 7) % 7
		if at := morning(days); days > 0 || at.After(now) {
			return at, true
		}
		return morning(7), true
	}
	return time.Time{}, false
}

// agentSettingFlags collects the --model and --reasoning choices given.
func agentSettingFlags(modelName, reasoning string) map[string]string {
	settings := map[string]string{}
	if modelName = strings.TrimSpace(modelName); modelName != "" {
		settings[driverprotocol.SettingModel] = modelName
	}
	if reasoning = strings.TrimSpace(reasoning); reasoning != "" {
		settings[driverprotocol.SettingReasoning] = reasoning
	}
	if len(settings) == 0 {
		return nil
	}
	return settings
}
