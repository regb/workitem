package primaryagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/model"
)

func TestExecutionCWDPrefersCurrentCheckoutOverHistoricalConversationPath(t *testing.T) {
	current := filepath.Join(t.TempDir(), "slot-0002")
	m := model.Manifest{Repository: model.Repository{RootAtCreation: "/repo"}, Checkout: model.Checkout{Specification: model.CheckoutSpecification{}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: current}}}
	session := model.Conversation{}
	if got := ExecutionCWD(m, session); got != current {
		t.Fatalf("ExecutionCWD = %q, want %q", got, current)
	}
}

func TestExecutionCWDFallsBackWithoutCheckout(t *testing.T) {
	m := model.Manifest{Repository: model.Repository{RootAtCreation: "/repo"}, Checkout: model.Checkout{}}
	if got := ExecutionCWD(m, model.Conversation{}); got != "/repo" {
		t.Fatalf("repository fallback = %q", got)
	}
}

func TestCheckoutContainsPhysicalCWD(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "slot")
	outside := filepath.Join(dir, "outside")
	alias := filepath.Join(dir, "logical")
	for _, p := range []string{root, outside} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !CheckoutContainsPath(alias, physical) || !CheckoutContainsPath(physical, alias) {
		t.Fatal("physical/logical CWD mismatch")
	}
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if CheckoutContainsPath(root, escape) {
		t.Fatal("outside symlink accepted")
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
