package daemonapi_test

import (
	"os/exec"
	"strings"
	"testing"
)

func integrationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-c", "user.name=Review test", "-c", "user.email=review@example.invalid", "-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
