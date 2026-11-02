package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/model"
)

func TestAttachRecordsThePathInsideTheCheckout(t *testing.T) {
	checkout := t.TempDir()
	m := model.Manifest{}
	if got := checkoutPath(m, filepath.Join(checkout, "DESIGN.md")); got != "" {
		t.Fatalf("path without a checkout: %q", got)
	}
	m.Checkout.Claim = &model.WorkspaceClaim{Path: checkout}
	if err := os.MkdirAll(filepath.Join(checkout, "doc", "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		filepath.Join(checkout, "DESIGN.md"):          "DESIGN.md",
		filepath.Join(checkout, "doc", "tmp", "X.md"): "doc/tmp/X.md",
		filepath.Join(checkout, ".git", "config"):     "",
		filepath.Join(t.TempDir(), "elsewhere.md"):    "",
	} {
		if got := checkoutPath(m, file); got != want {
			t.Errorf("checkoutPath(%s) = %q, want %q", file, got, want)
		}
	}
}
