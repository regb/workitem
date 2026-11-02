package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// LocalAccount identifies a native configuration slot, not a verified billing
// identity. A slot may be used by different native logins over time.
func LocalAccount(provider, root string) Account {
	return Account{Provider: provider, ID: provider + ":" + fingerprint(filepath.Clean(root))[:16]}
}
func fingerprint(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func readSmall(path string) ([]byte, error) {
	// Do not follow a cache-file symlink or block on a FIFO/device. Inspect the
	// opened descriptor so replacing the path cannot redirect the read.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(b) > 1<<20 {
		return nil, fmt.Errorf("file too large")
	}
	return b, err
}
