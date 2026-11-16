package workspacefiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

const MaxDocumentBytes = 16 << 20

var ErrDocument = errors.New("only PDF documents up to 16 MiB can be opened")

// ReadDocument takes a bounded snapshot through the same no-follow directory
// walk as source reads. It never serves arbitrary active web content.
func ReadDocument(root, p string) ([]byte, string, error) {
	if !strings.EqualFold(path.Ext(p), ".pdf") {
		return nil, "", ErrDocument
	}
	dir, name, err := parent(root, p)
	if errors.Is(err, errMissingParent) {
		return nil, "", os.ErrNotExist
	}
	if err != nil {
		return nil, "", err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open document without symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxDocumentBytes {
		return nil, "", ErrDocument
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > MaxDocumentBytes || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, "", ErrDocument
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}
