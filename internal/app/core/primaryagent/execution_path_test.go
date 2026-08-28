package primaryagent

import (
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/model"
)

func TestExecutionCWDPrefersCurrentCheckoutOverHistoricalConversationPath(t *testing.T) {
	current := filepath.Join(t.TempDir(), "slot-0002")
	m := model.Manifest{Repository: model.Repository{RootAtCreation: "/repo"}, Checkout: model.Checkout{Path: &current}}
	session := model.PiSession{}
	if got := ExecutionCWD(m, session); got != current {
		t.Fatalf("ExecutionCWD = %q, want %q", got, current)
	}
}

func TestExecutionCWDFallsBackWithoutCheckout(t *testing.T) {
	m := model.Manifest{Repository: model.Repository{RootAtCreation: "/repo"}, Checkout: model.Checkout{}}
	if got := ExecutionCWD(m, model.PiSession{}); got != "/repo" {
		t.Fatalf("repository fallback = %q", got)
	}
}

func TestCheckoutContainsPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "slot-0002")
	if !CheckoutContainsPath(root, filepath.Join(root, "internal", "app")) {
		t.Fatal("expected checkout subdirectory to match")
	}
	if CheckoutContainsPath(root, filepath.Join(filepath.Dir(root), "slot-0004")) {
		t.Fatal("different slot must not match")
	}
}
