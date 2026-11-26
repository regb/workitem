package oneshot

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
)

func command(path string) Command {
	return Command{Name: "Tool", Path: path, Args: func(string) ([]string, error) { return nil, nil }}
}

func TestRunIsolatesTheTool(t *testing.T) {
	t.Setenv("WI_ITEM_ID", "item")
	t.Setenv("DIRENV_DIFF", "x")
	t.Setenv("OWN_SESSION", "x")
	t.Setenv("KEEP_ME", "kept")
	cwd, _ := os.Getwd()
	path := testutil.Script(t, `[ "$PWD" != "`+cwd+`" ] || exit 3
[ -z "$(ls -A)" ] || exit 4
env | grep -q '^WI_\|^DIRENV_\|^OWN_SESSION=' && exit 5
printf '%s %s %s\n' "$KEEP_ME" "$ADDED" "$(cat)"`)
	c := command(path)
	c.Stdin, c.Drop, c.Env = "prompt", []string{"OWN_SESSION"}, map[string]string{"ADDED": "added"}
	if out, err := Run(context.Background(), c); err != nil || out != "kept added prompt" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestRunFailures(t *testing.T) {
	for body, want := range map[string]string{
		"printf ''": "no answer",
		"echo 'error: not signed in' >&2; exit 1": "not signed in",
		`head -c 20000 /dev/zero | tr '\0' x`:     "more than",
	} {
		if _, err := Run(context.Background(), command(testutil.Script(t, body))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Run(ctx, command(testutil.Script(t, "sleep 10 & wait"))); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation: %v after %s", err, time.Since(start))
	}
}
