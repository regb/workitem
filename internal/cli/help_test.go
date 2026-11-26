package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestAllPublicHelpTopicsAreDocumented(t *testing.T) {
	topics := []string{
		"config", "config show", "config get", "config drivers", "config plugins", "version", "new", "list", "show", "events", "start", "switch", "next", "resume", "merge", "shelve", "close", "wait", "blocker", "blocker add", "blocker list", "blocker clear", "pick", "plugin", "reopen", "delete", "shutdown", "edit", "note", "notes", "attach", "attachments", "attachment", "attachment cat",
		"repo", "repo list", "repo rename", "repo archive", "repo restore", "repo settings", "repo settings show", "repo settings set", "repo settings unset",
		"state", "state show", "state set",
		"attention", "attention activity", "attention defer", "attention queue", "attention read", "attention unread",
		"workspace", "workspace status", "workspace ensure", "workspace release",
		"terminal", "terminal status", "terminal ensure", "terminal enter", "terminal close",
		"agent", "agent status", "agent control", "agent control abort", "agent control shutdown",
		"agent runtime", "agent runtime status", "agent runtime ensure", "agent runtime stop", "agent monitor",
	}
	for _, topic := range topics {
		t.Run(topic, func(t *testing.T) {
			doc, ok := helpDocs[topic]
			if !ok {
				t.Fatalf("missing help topic %q", topic)
			}
			if strings.TrimSpace(doc.Summary) == "" || len(doc.Usage) == 0 {
				t.Fatalf("incomplete help topic %q: %+v", topic, doc)
			}
			var out bytes.Buffer
			if !printHelp(&out, topic) || !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "--help") {
				t.Fatalf("invalid rendered help for %q:\n%s", topic, out.String())
			}
		})
	}
}
