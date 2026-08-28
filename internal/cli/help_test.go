package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestGlobalHelpGroupsCommands(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)
	text := out.String()
	sections := []string{"Work:", "Metadata and history:", "Agent and checkout:", "Administration:"}
	last := -1
	for _, section := range sections {
		index := strings.Index(text, section)
		if index < 0 {
			t.Fatalf("global help is missing %q:\n%s", section, text)
		}
		if index <= last {
			t.Fatalf("global help section %q is out of order:\n%s", section, text)
		}
		last = index
	}
}

func TestAllPublicHelpTopicsAreDocumented(t *testing.T) {
	topics := []string{
		"version", "new", "list", "show", "events", "start", "switch", "next", "resume", "merge", "shelve", "archive", "delete", "shutdown", "label", "deep",
		"state", "state show", "state set",
		"attention", "attention activity", "attention defer", "attention queue",
		"workspace", "workspace status", "workspace ensure", "workspace release",
		"terminal", "terminal status", "terminal ensure", "terminal enter", "terminal close",
		"agent", "agent status", "agent control", "agent control send", "agent control abort", "agent control shutdown",
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
