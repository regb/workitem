package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/app"
)

// contentRetryInterval paces resubscribing while no runtime streams content.
const contentRetryInterval = time.Second

// runContentMonitor follows an item's live content until interrupted. When no
// runtime is live, or one stops, it waits and follows the next one.
func runContentMonitor(ctx context.Context, cfg Config, resolve app.ResolveOptions, itemID string, jsonOut bool) error {
	printer := &contentPrinter{w: cfg.Stdout, json: jsonOut}
	if !jsonOut {
		fmt.Fprintf(cfg.Stdout, "work item: %s\n", itemID)
	}
	waiting := ""
	for {
		err := cfg.App.AgentContentStream(ctx, resolve, func(event driverprotocol.ContentEvent) error {
			waiting = ""
			return printer.print(event)
		})
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, driverprotocol.ErrContentStreamUnsupported) {
			return fmt.Errorf("the running agent's driver cannot stream content; it streams after its next start")
		}
		if err != nil && err.Error() != waiting && !jsonOut {
			// Say once why there is nothing to follow, not on every retry.
			waiting = err.Error()
			printer.endLine()
			fmt.Fprintf(cfg.Stdout, "waiting for live content: %s\n", waiting)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(contentRetryInterval):
		}
	}
}

// contentPrinter writes content events for a terminal: message text as it
// streams, and one line per tool, activity or lifecycle change. With JSON it
// writes one compact event per line.
type contentPrinter struct {
	w       io.Writer
	json    bool
	midLine bool
	inText  string // "text" or "thought" while streaming one of them
}

func (p *contentPrinter) endLine() {
	if p.midLine {
		fmt.Fprintln(p.w)
		p.midLine = false
	}
	p.inText = ""
}

func (p *contentPrinter) line(format string, args ...any) {
	p.endLine()
	fmt.Fprintf(p.w, format+"\n", args...)
}

// stream writes text inline, starting a labelled run when its kind changes.
func (p *contentPrinter) stream(kind, label, text string) {
	if p.inText != kind {
		p.endLine()
		fmt.Fprint(p.w, label)
		p.inText = kind
	}
	fmt.Fprint(p.w, text)
	p.midLine = !strings.HasSuffix(text, "\n")
}

func (p *contentPrinter) print(event driverprotocol.ContentEvent) error {
	if p.json {
		return json.NewEncoder(p.w).Encode(event)
	}
	switch event.Type {
	case driverprotocol.ContentSnapshot:
		value := event.Snapshot
		p.line("runtime: %s  activity: %s", value.RuntimeID, orNone(value.Activity))
		for _, tool := range value.Tools {
			p.line("tool %s: %s%s", tool.Status, tool.Name, toolDetail(tool))
		}
		// A finished message stays in the snapshot until the next starts; only
		// text still being written is news.
		if value.Text != "" && len(value.SavedAs) == 0 && value.Activity != "Idle" {
			p.stream("text", "", value.Text)
		}
	case driverprotocol.ContentMessageStarted, driverprotocol.ContentMessageCompleted:
		p.endLine()
	case driverprotocol.ContentMessageDelta:
		p.stream("text", "", event.Text)
	case driverprotocol.ContentMessageReplaced:
		p.line("text replaced:")
		p.stream("text", "", event.Text)
	case driverprotocol.ContentThoughtDelta:
		p.stream("thought", "thinking: ", event.Text)
	case driverprotocol.ContentMessageSaved:
		p.line("saved as %s", strings.Join(event.NativeIDs, ", "))
	case driverprotocol.ContentToolStarted, driverprotocol.ContentToolUpdated, driverprotocol.ContentToolCompleted:
		if event.Tool != nil && event.Type != driverprotocol.ContentToolUpdated {
			p.line("tool %s: %s%s", event.Tool.Status, event.Tool.Name, toolDetail(*event.Tool))
		}
	case driverprotocol.ContentActivity:
		p.line("activity: %s", orNone(event.Activity))
	case driverprotocol.ContentTurnSettled:
		p.line("turn settled")
	}
	return nil
}

// toolDetail is the most telling argument of a tool's JSON input (a command,
// path or pattern), or the first line of input that is not JSON.
func toolDetail(tool driverprotocol.ContentTool) string {
	input := strings.TrimSpace(tool.Input)
	var fields map[string]any
	if json.Unmarshal([]byte(input), &fields) == nil {
		input = ""
		for _, key := range []string{"command", "cmd", "file_path", "path", "pattern", "query", "url", "description"} {
			if value, ok := fields[key].(string); ok && value != "" {
				input = value
				break
			}
		}
	}
	first, _, _ := strings.Cut(input, "\n")
	if first = strings.TrimSpace(first); first == "" {
		return ""
	}
	return "  " + truncateRunes(first, 100)
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
