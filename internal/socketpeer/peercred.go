// Package socketpeer authenticates the local user at both ends of a Unix socket.
package socketpeer

import "fmt"

func validatePeerUID(peerUID, currentUID uint32) error {
	if peerUID != currentUID {
		return fmt.Errorf("reject daemon peer owned by uid %d", peerUID)
	}
	return nil
}
