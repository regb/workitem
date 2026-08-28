package primaryagent

import (
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/model"
)

// ExecutionCWD keeps a runtime anchored to the item's current checkout.
func ExecutionCWD(m model.Manifest, _ model.PiSession) string {
	if m.Checkout.Present() && m.Checkout.Path != nil {
		if path := strings.TrimSpace(*m.Checkout.Path); path != "" {
			return path
		}
	}
	return strings.TrimSpace(m.Repository.RootAtCreation)
}

func CheckoutContainsPath(checkoutPath, candidate string) bool {
	checkoutPath = strings.TrimSpace(checkoutPath)
	candidate = strings.TrimSpace(candidate)
	if checkoutPath == "" || candidate == "" {
		return false
	}
	root, err := filepath.Abs(checkoutPath)
	if err != nil {
		return false
	}
	path, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
