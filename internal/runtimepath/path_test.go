package runtimepath_test

import (
	"strings"
	"testing"

	"github.com/regb/workitem/internal/runtimepath"
)

func TestControlSocketPathIsBounded(t *testing.T) {
	path := runtimepath.ControlSocket("01KZYHGDCECSFS4BJ2SNTQP49V", strings.Repeat("runtime-", 30))
	if len(path) > 64 || !strings.HasSuffix(path, ".sock") {
		t.Fatalf("path = %q (%d bytes)", path, len(path))
	}
}
