package executor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
)

func TestAuthorityControlPathIsBoundedPerExecutor(t *testing.T) {
	client := Client{Name: "macbook", MasterID: strings.Repeat("master", 40), Definition: config.ExecutorConfig{Destination: "user@example.com"}}
	path := client.authorityControlPath()
	if !filepath.IsAbs(path) || len(path) >= 100 {
		t.Fatalf("control path is not portable: %q", path)
	}
	other := client
	other.Name = "linux"
	if other.authorityControlPath() == path {
		t.Fatal("different executors share an authority control path")
	}
}
