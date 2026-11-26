// Package runwi is the TestMain of a package whose apitest fixtures run
// agents. It is apart from apitest so that wi's own packages may use
// apitest without importing wi.
package runwi

import (
	"context"
	"os"
	"testing"

	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/testutil"
	"github.com/regb/workitem/internal/wimain"
)

// Main runs the tests isolated, as testutil.RunIsolatedTests does. A daemon
// accepts only runtime hosts of its own build, and a fixture's daemon is
// the test binary: so the test binary is also the wi executable the daemon
// starts runtime hosts and drivers from. Started that way, with
// apitest.RunAsWi set, it runs wi instead of the tests.
func Main(m *testing.M) int {
	if os.Getenv(apitest.RunAsWi) == "1" {
		return wimain.Run(context.Background(), os.Args[1:])
	}
	return testutil.RunIsolatedTests(m.Run)
}
