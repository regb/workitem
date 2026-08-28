package coordinator

import "fmt"

func validatePeerUID(peerUID, currentUID uint32) error {
	if peerUID != currentUID {
		return fmt.Errorf("reject daemon peer owned by uid %d", peerUID)
	}
	return nil
}
