package pi

import (
	"fmt"
	"os"
	"path/filepath"
)

const maxConversationSnapshotBytes = 256 << 20

func exportConversation(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect Pi conversation for export: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxConversationSnapshotBytes {
		return nil, fmt.Errorf("Pi conversation export must be a regular file no larger than 256 MiB")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Pi conversation for export: %w", err)
	}
	return content, nil
}

func importConversation(path string, content []byte) error {
	if len(content) > maxConversationSnapshotBytes {
		return fmt.Errorf("Pi conversation import exceeds 256 MiB")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Pi conversation directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".conversation-import-*.tmp")
	if err != nil {
		return fmt.Errorf("create Pi conversation import: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write Pi conversation import: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync Pi conversation import: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Pi conversation import: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish Pi conversation import: %w", err)
	}
	return nil
}
