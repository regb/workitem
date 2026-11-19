package workspacefiles

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// SnapshotFile reads bytes or the literal target of a symlink through pinned
// directories. A directory, device, or pipe is never followed or read.
func SnapshotFile(root, p string, limit int64) (data []byte, mode string, exists bool, err error) {
	dir, name, err := parent(root, p)
	if errors.Is(err, errMissingParent) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	defer dir.Close()
	var st unix.Stat_t
	if err = unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		buf := make([]byte, 8192)
		n, e := unix.Readlinkat(int(dir.Fd()), name, buf)
		if e != nil {
			return nil, "", true, e
		}
		if n == len(buf) {
			return nil, "", true, errors.New("symlink target exceeds capture limit")
		}
		return buf[:n], "120000", true, nil
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, "", true, errors.New("unsupported non-regular snapshot path")
	}
	fd, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, "", true, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, "", true, e
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, "", true, errors.New("snapshot file exceeds capture limit or changed type")
	}
	data, e = io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, "", true, e
	}
	if int64(len(data)) > limit {
		return nil, "", true, errors.New("snapshot file exceeds capture limit")
	}
	mode = "100644"
	if info.Mode().Perm()&0111 != 0 {
		mode = "100755"
	}
	return data, mode, true, nil
}
