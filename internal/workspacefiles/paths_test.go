package workspacefiles

import (
	"fmt"
	"reflect"
	"testing"
)

func TestPathsAreValidSortedUniqueAndBounded(t *testing.T) {
	paths, truncated := Paths([]string{"b.go", "a.go", "b.go", "../escape", ".git/config", "/abs", "dir/c.go"})
	if truncated || !reflect.DeepEqual(paths, []string{"a.go", "b.go", "dir/c.go"}) {
		t.Fatalf("%v %v", paths, truncated)
	}
	many := make([]string, MaxListedPaths+1)
	for i := range many {
		many[i] = fmt.Sprintf("f%05d", i)
	}
	if paths, truncated := Paths(many); !truncated || len(paths) != MaxListedPaths {
		t.Fatalf("%d %v", len(paths), truncated)
	}
}
