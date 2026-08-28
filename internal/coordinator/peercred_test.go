package coordinator

import "testing"

func TestValidatePeerUID(t *testing.T) {
	if err := validatePeerUID(1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := validatePeerUID(1001, 1000); err == nil {
		t.Fatal("mismatched peer uid was accepted")
	}
}
