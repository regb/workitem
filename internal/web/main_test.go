package web

import (
	"os"
	"testing"

	"github.com/regb/workitem/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunIsolatedTests(m.Run))
}
