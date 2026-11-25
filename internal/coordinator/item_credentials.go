package coordinator

import (
	"encoding/hex"
	"errors"

	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// ItemCredentialProjection holds, per item, the hash of the credential its
// current agent runtime was given. Only the daemon writes it: it is not a
// store command, so runtime hosts cannot publish it, and it is never mirrored
// to the item's files or recorded in events.
const ItemCredentialProjection = "item_credentials"

// ItemCredential is the hash an item's agent runtime proves itself with.
type ItemCredential struct {
	RuntimeID string `json:"runtime_id"`
	Hash      string `json:"hash"`
}

// SaveItemCredential replaces an item's credential with one for runtimeID.
// The previous runtime's credential stops working at once.
func (d *Database) SaveItemCredential(itemID, runtimeID string, hash [32]byte) error {
	if !model.ValidID(itemID) || runtimeID == "" {
		return errors.New("item credential needs an item and a runtime")
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		return putProjectionValue(tx, ItemCredentialProjection, itemID, ItemCredential{RuntimeID: runtimeID, Hash: hex.EncodeToString(hash[:])})
	})
}

// VerifyItemCredential reports whether value is the credential of the item's
// current agent runtime: the one the item's runtime ownership names, until
// its process ends. A credential outlives neither its runtime nor a newer
// one taking the item over.
func (d *Database) VerifyItemCredential(value credential.Credential) (bool, error) {
	if value.Kind != credential.Item {
		return false, nil
	}
	var stored ItemCredential
	found, err := d.ReadProjection(ItemCredentialProjection, value.ItemID, &stored)
	if err != nil || !found {
		return false, err
	}
	hash, err := hex.DecodeString(stored.Hash)
	if err != nil || len(hash) != 32 || !value.Matches([32]byte(hash)) {
		return false, err
	}
	var runtime model.AgentRuntime
	found, err = d.ReadProjection(RuntimeOwnershipProjection, value.ItemID, &runtime)
	if err != nil || !found {
		return false, err
	}
	return runtime.ID == stored.RuntimeID && runtime.StoppedAt == nil && runtime.State != model.AgentRuntimeStopped, nil
}
