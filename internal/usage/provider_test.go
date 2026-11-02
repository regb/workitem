package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func floatPtr(v float64) *float64 { return &v }
func TestAccountAndNativeWindow(t *testing.T) {
	if LocalAccount("claude", "/a") == LocalAccount("codex", "/a") || LocalAccount("claude", "/a") == LocalAccount("claude", "/b") {
		t.Fatal("slot identities collide")
	}
	value := 25.5
	reset := testNow.Unix()
	duration := int64(300)
	if w := NativeWindow("five_hour", &value, &reset, &duration); w == nil || w.RemainingPercent == nil || *w.RemainingPercent != 100-value || !w.ResetsAt.Equal(testNow) {
		t.Fatal(w)
	}
	value = -1
	if w := NativeWindow("bad", &value, nil, nil); w != nil {
		t.Fatal(w)
	}
}
