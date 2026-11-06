package coordinator

import (
	"net"

	"github.com/regb/workitem/internal/socketpeer"
)

func validatePeerOwnership(conn *net.UnixConn) error { return socketpeer.Validate(conn) }
