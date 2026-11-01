package setup_test

import (
	"github.com/regb/workitem/internal/testutil"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolatedTests(m.Run)) }
