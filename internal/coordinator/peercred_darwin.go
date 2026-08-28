//go:build darwin

package coordinator

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func validatePeerOwnership(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("inspect daemon peer: %w", err)
	}
	var credential *unix.Xucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return fmt.Errorf("inspect daemon peer: %w", err)
	}
	if socketErr != nil {
		return fmt.Errorf("inspect daemon peer credentials: %w", socketErr)
	}
	if credential == nil {
		return fmt.Errorf("reject daemon peer with missing credentials")
	}
	return validatePeerUID(credential.Uid, uint32(os.Geteuid()))
}
