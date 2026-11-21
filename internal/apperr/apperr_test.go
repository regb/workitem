package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestKindsSurviveWrappingAndNaming(t *testing.T) {
	err := fmt.Errorf("save: %w", Stalef("item revision conflict: expected %d", 3))
	if !errors.Is(err, Stale) || errors.Is(err, Invalid) || err.Error() != "save: item revision conflict: expected 3" {
		t.Fatalf("%v", err)
	}
	if KindOf(err) != "stale" || ByName(KindOf(err)) != Stale || KindOf(errors.New("plain")) != "" || ByName("") != nil {
		t.Fatalf("kind %q", KindOf(err))
	}
}
