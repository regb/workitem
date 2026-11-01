package model

import (
	"reflect"
	"testing"
)

func TestMergeEnvKeepsOrder(t *testing.T) {
	before := []EnvVar{{"A", "1"}, {"B", "2"}, {"C", "3"}}
	after := MergeEnv(before, []EnvVar{{"B", "two"}, {"D", "4"}}, []string{"A"})
	if want := []EnvVar{{"B", "two"}, {"C", "3"}, {"D", "4"}}; !reflect.DeepEqual(after, want) {
		t.Fatal(after)
	}
	if changes := EnvChanges(before, after); !reflect.DeepEqual(changes, []string{"B", "D", "A"}) {
		t.Fatal(changes)
	}
	if changes := EnvChanges(before, []EnvVar{{"C", "3"}, {"B", "2"}, {"A", "1"}}); len(changes) != 0 {
		t.Fatal("a reordering changed", changes)
	}
}
