package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// Script writes an executable shell script with body under a new temporary
// directory and returns its path.
func Script(t testing.TB, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	WriteScript(t, path, body)
	return path
}

// WriteScript writes an executable shell script with body at path.
func WriteScript(t testing.TB, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}
