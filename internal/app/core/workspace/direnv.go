package workspace

import (
	"context"
	"strings"
)

// revokeReusedSlotDirenvTrust ensures trust does not silently carry across
// branches merely because a reusable slot path and its top-level .envrc bytes
// stayed the same. Runtime startup will ask again, or apply the user's explicit
// repository auto-trust policy.
func (s *Service) revokeReusedSlotDirenvTrust(ctx context.Context, targetDir string) (bool, error) {
	if s.direnv == nil || strings.TrimSpace(targetDir) == "" {
		return false, nil
	}
	status, err := s.direnv.Status(ctx, targetDir)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file or directory") {
			return false, nil
		}
		return false, err
	}
	if !status.Found || !status.Allowed || strings.TrimSpace(status.RCPath) == "" {
		return false, nil
	}
	if err := s.direnv.Deny(ctx, status.RCPath); err != nil {
		return false, err
	}
	return true, nil
}
