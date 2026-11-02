package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeHelperPathsCannotEscapePrivateDirectory(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "unchanged")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, "../escape", "sub/../../escape", ""} {
		_, err := RunNativeCommandWithFiles(context.Background(), []string{"must-not-execute"}, nil, nil, map[string][]byte{path: []byte("replace")})
		if err == nil || err.Error() != "invalid native helper path" {
			t.Fatal(path, err)
		}
	}
	b, err := os.ReadFile(outside)
	if err != nil || string(b) != "original" {
		t.Fatal(string(b), err)
	}
}
