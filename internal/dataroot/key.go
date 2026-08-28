package dataroot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

func Key(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("data root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve data root: %w", err)
	}
	digest := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return hex.EncodeToString(digest[:8]), nil
}
