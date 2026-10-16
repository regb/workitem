package extension

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/regb/workitem/internal/app/contract"
)

func TestEnvironmentHooksAreOrderedIsolatedAndProtectIdentity(t *testing.T) {
	source := map[string]string{"BASE": "original"}
	settings := map[string]any{"labels": []string{"unchanged"}}
	order := []string{}
	p := Pipeline{Options: func(_ context.Context, repo, name string) (map[string]any, error) {
		if repo != "repo-id" {
			t.Fatal(repo)
		}
		return settings, nil
	}, Hooks: []Hooks{
		{Name: "first", RuntimeEnvironment: func(ctx context.Context, r EnvironmentRequest, o map[string]any) (Patch, error) {
			order = append(order, "first")
			r.Values["BASE"] = "mutated"
			r.Source["BASE"] = "mutated"
			o["labels"].([]string)[0] = "mutated"
			return Patch{Set: map[string]string{"ADDED": "one", "WI_ID": "wrong", "TMUX": "wrong"}}, nil
		}},
		{Name: "second", RuntimeEnvironment: func(ctx context.Context, r EnvironmentRequest, o map[string]any) (Patch, error) {
			order = append(order, "second")
			if r.Values["ADDED"] != "one" || r.Values["BASE"] != "original" || o["labels"].([]string)[0] != "unchanged" {
				t.Fatal("mutable hook state leaked")
			}
			return Patch{Set: map[string]string{"ADDED": "two"}}, nil
		}}}}
	values, _, warnings, err := p.Environment(context.Background(), "runtime", EnvironmentRequest{Subject: Subject{RepositoryID: "repo-id"}, Values: source, Source: source})
	if err != nil || values["ADDED"] != "two" || values["WI_ID"] != "" || values["TMUX"] != "" || len(warnings) != 1 || !reflect.DeepEqual(order, []string{"first", "second"}) || source["BASE"] != "original" {
		t.Fatalf("order=%v warnings=%v err=%v", order, warnings, err)
	}
}
func TestHookCancellationAndFailurePreventNextEffects(t *testing.T) {
	for _, cancelDuring := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		called := false
		p := Pipeline{Hooks: []Hooks{{Name: "first", PrepareCheckout: func(context.Context, contract.CheckoutPreparation, map[string]any) ([]string, error) {
			if cancelDuring {
				cancel()
				return nil, nil
			}
			return []string{"earlier step retained"}, errors.New("failure")
		}}, {Name: "second", PrepareCheckout: func(context.Context, contract.CheckoutPreparation, map[string]any) ([]string, error) {
			called = true
			return nil, nil
		}}}}
		if _, err := p.PrepareCheckout(ctx, contract.CheckoutPreparation{}); err == nil || called {
			t.Fatal("continued after failed/cancelled preparation")
		}
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := Pipeline{Hooks: []Hooks{{Name: "cancel", RuntimeEnvironment: func(context.Context, EnvironmentRequest, map[string]any) (Patch, error) {
		cancel()
		return Patch{Set: map[string]string{"VALUE": "not applied"}}, nil
	}}}}
	if values, _, _, err := p.Environment(ctx, "runtime", EnvironmentRequest{}); err == nil || values != nil {
		t.Fatal("applied cancelled environment")
	}
}
func TestPipelineRejectsDuplicateHooksAndInvalidPatches(t *testing.T) {
	p := Pipeline{Hooks: []Hooks{{Name: "same"}, {Name: "same"}}}
	if _, err := p.PrepareCheckout(context.Background(), contract.CheckoutPreparation{}); err == nil {
		t.Fatal("duplicate hooks")
	}
	for _, patch := range []Patch{{Set: map[string]string{"BAD=KEY": "x"}}, {Set: map[string]string{"KEY": "bad\x00value"}}, {Unset: []string{"WI_ID"}}} {
		p = Pipeline{Hooks: []Hooks{{Name: "invalid", RuntimeEnvironment: func(context.Context, EnvironmentRequest, map[string]any) (Patch, error) { return patch, nil }}}}
		if _, _, _, err := p.Environment(context.Background(), "runtime", EnvironmentRequest{}); err == nil {
			t.Fatal("invalid patch accepted")
		}
	}
	p = Pipeline{Hooks: []Hooks{{Name: "invalid", CommandEnvironment: func(context.Context, EnvironmentRequest, map[string]any) (Patch, error) {
		return Patch{Set: map[string]string{"PATH": "/wrong"}}, nil
	}}}}
	values, _, warnings, err := p.Environment(context.Background(), "command", EnvironmentRequest{Values: map[string]string{"PATH": "/safe"}, AllowedKeys: []string{"WI_LIST_LABELS"}})
	if err != nil || values["PATH"] != "/safe" || len(warnings) != 1 {
		t.Fatal(values, warnings, err)
	}
}
