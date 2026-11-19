package git

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// Capture authority comes from the claim's path and registered repository, not
// the immutable item branch specification. Do not follow a replaced .git into
// a different repository or treat a nested directory as the claimed checkout.
func (c Client) ValidateReviewCheckout(ctx context.Context, repository, checkout string) error {
	expected, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return err
	}
	claimed, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return err
	}
	common, err := c.snapshotCommand(ctx, checkout, "", nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(strings.TrimSpace(string(common)))
	if err != nil {
		return err
	}
	top, err := c.snapshotCommand(ctx, checkout, "", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil {
		return err
	}
	if filepath.Clean(actual) != filepath.Clean(expected) || filepath.Clean(root) != filepath.Clean(claimed) {
		return errors.New("claimed checkout does not belong to the registered review repository")
	}
	return nil
}
