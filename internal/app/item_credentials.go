package app

import "github.com/regb/workitem/internal/credential"

// ItemCredentialStore keeps the hash of each item's current runtime
// credential; the daemon binds it to its database.
type ItemCredentialStore interface {
	SaveItemCredential(itemID, runtimeID string, hash [32]byte) error
}

// itemCredentials mints a runtime's credential, writes it where only that
// runtime's agent is told to look, and records its hash. Recording it ends
// the previous runtime's credential.
type itemCredentials struct{ store ItemCredentialStore }

func (c itemCredentials) IssueItemCredential(itemID, runtimeID, path string) error {
	value, err := credential.New(credential.Item, itemID)
	if err != nil {
		return err
	}
	if err := credential.Write(path, value); err != nil {
		return err
	}
	return c.store.SaveItemCredential(itemID, runtimeID, value.Hash())
}
