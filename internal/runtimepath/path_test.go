package runtimepath_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/runtimepath"
)

func TestControlSocketPathIsBounded(t *testing.T) {
	path := runtimepath.ControlSocket("01KZYHGDCECSFS4BJ2SNTQP49V", strings.Repeat("runtime-", 30))
	if len(path) > 64 || !strings.HasSuffix(path, ".sock") {
		t.Fatalf("path = %q (%d bytes)", path, len(path))
	}
}

func TestEventSocketDiffersFromControlSocket(t *testing.T) {
	itemID, runtimeID := "01KZYHGDCECSFS4BJ2SNTQP49V", strings.Repeat("runtime-", 30)
	control, event := runtimepath.ControlSocket(itemID, runtimeID), runtimepath.EventSocket(itemID, runtimeID)
	if control == event || len(event) > 72 || !strings.HasSuffix(event, ".sock") {
		t.Fatalf("control=%q event=%q", control, event)
	}
}

func TestPortableSocketPathKeepsDataRootsIsolated(t *testing.T) {
	relative := runtimepath.ControlSocket("item-1", "runtime-1")
	candidate := filepath.Join("/private/var/folders", strings.Repeat("long-runtime-root", 8), filepath.FromSlash(relative))
	first := runtimepath.PortableSocketPath("darwin", candidate, "/data/root-one", relative)
	second := runtimepath.PortableSocketPath("darwin", candidate, "/data/root-two", relative)
	if first == candidate || second == candidate {
		t.Fatalf("long Darwin path did not use fallback: first=%q second=%q", first, second)
	}
	if first == second {
		t.Fatalf("different data roots share fallback path %q", first)
	}
	if again := runtimepath.PortableSocketPath("darwin", candidate, "/data/root-one", relative); again != first {
		t.Fatalf("fallback path is unstable: %q != %q", again, first)
	}
	if len(first) >= 100 || len(second) >= 100 {
		t.Fatalf("fallback paths remain too long: first=%d second=%d", len(first), len(second))
	}
}
