package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

func TestRetiredCheckoutKindCannotBeMaterializedOrReleased(t *testing.T) {
	for _, present := range []bool{false, true} {
		m := model.Manifest{ID: "item-1", State: model.StateActive, Checkout: model.Checkout{Specification: model.CheckoutSpecification{Kind: "repository-home", Executor: model.ExecutorLocal}}}
		path := t.TempDir()
		file := filepath.Join(path, "user-file")
		if err := os.WriteFile(file, []byte("user work\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if present {
			if _, err := m.Checkout.AssignNewClaim(model.ExecutorLocal, "repo", "", path); err != nil {
				t.Fatal(err)
			}
		}
		resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return m, nil }
		st := &recordingStore{}
		s := New(st, fakeGit{}, nil, nil, resolve, nil, nil)
		ctx := context.Background()
		if _, err := s.CheckoutCreate(ctx, contract.ResolveOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported checkout kind") {
			t.Fatalf("present=%v create error=%v", present, err)
		}
		if _, _, err := s.EnsureCheckout(ctx, contract.ResolveOptions{}, m, true); err == nil || !strings.Contains(err.Error(), "unsupported checkout kind") {
			t.Fatalf("present=%v ensure error=%v", present, err)
		}
		if present {
			if _, err := s.Release(ctx, contract.ResolveOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported checkout kind") {
				t.Fatalf("release error=%v", err)
			}
			if _, err := s.ReleaseCheckout(ctx, m); err == nil || !strings.Contains(err.Error(), "unsupported checkout kind") {
				t.Fatalf("release checkout error=%v", err)
			}
		}
		if st.saved.ID != "" || len(st.events) != 0 {
			t.Fatalf("wrote retired checkout: saved=%+v events=%v", st.saved, st.events)
		}
		contents, err := os.ReadFile(file)
		if err != nil || string(contents) != "user work\n" {
			t.Fatalf("external checkout changed: contents=%q error=%v", contents, err)
		}
	}
}
