// Package credential mints, parses and stores the secrets callers present to
// the daemon API. Every call carries one: the operator's, the web proxy's, or
// an item's, which its agent runtime receives. The daemon keeps only hashes
// of item credentials; the operator and proxy credentials are files in the
// data root, readable by their Unix user only.
//
// A credential identifies a caller to a daemon that trusts its own Unix user.
// It is not isolation: an agent running as that user can read the operator's
// file. It keeps well-behaved agents acting as themselves, and becomes a
// boundary once an agent runs where the operator's file is not mounted.
package credential

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/model"
)

// Kind is what a credential proves.
type Kind string

const (
	// Operator is the local operator, the installation's user.
	Operator Kind = "operator"
	// Proxy is wi web: it acts for the user it names.
	Proxy Kind = "proxy"
	// Item is a work item's agent, while its runtime owns the item.
	Item Kind = "item"
)

const (
	prefix      = "wi_"
	secretBytes = 32
	// maxLength bounds a presented credential before any parsing.
	maxLength = 256
	// FileVariable names the file holding an agent's item credential, in the
	// environment of its runtime.
	FileVariable = "WI_ITEM_CREDENTIAL_FILE"
)

// ErrInvalid is a credential that is not one wi issued.
var ErrInvalid = errors.New("invalid wi credential")

// Credential is a parsed credential.
type Credential struct {
	Kind Kind
	// ItemID is the item an item credential belongs to.
	ItemID string
	token  string
}

// Token is the credential's text, as presented.
func (c Credential) Token() string { return c.token }

// Hash is what the daemon stores and compares.
func (c Credential) Hash() [32]byte { return sha256.Sum256([]byte(c.token)) }

// Matches reports, in constant time, whether the credential hashes to hash.
func (c Credential) Matches(hash [32]byte) bool {
	value := c.Hash()
	return subtle.ConstantTimeCompare(value[:], hash[:]) == 1
}

// New mints a credential. Item credentials name their item, so the daemon
// finds the stored hash in one lookup.
func New(kind Kind, itemID string) (Credential, error) {
	secret := make([]byte, secretBytes)
	if _, err := rand.Read(secret); err != nil {
		return Credential{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(secret)
	switch kind {
	case Operator, Proxy:
		if itemID != "" {
			return Credential{}, fmt.Errorf("%s credentials name no item", kind)
		}
		return Credential{Kind: kind, token: prefix + string(kind) + "_" + encoded}, nil
	case Item:
		if !model.ValidID(itemID) {
			return Credential{}, fmt.Errorf("invalid item id %q", itemID)
		}
		return Credential{Kind: kind, ItemID: itemID, token: prefix + string(kind) + "_" + itemID + "_" + encoded}, nil
	}
	return Credential{}, fmt.Errorf("unknown credential kind %q", kind)
}

// Parse reads a presented credential. It checks the form only; whether the
// daemon issued it is the daemon's comparison with what it stored.
func Parse(token string) (Credential, error) {
	if len(token) > maxLength || !strings.HasPrefix(token, prefix) {
		return Credential{}, ErrInvalid
	}
	kind, rest, ok := strings.Cut(strings.TrimPrefix(token, prefix), "_")
	if !ok {
		return Credential{}, ErrInvalid
	}
	value := Credential{Kind: Kind(kind), token: token}
	switch value.Kind {
	case Operator, Proxy:
	case Item:
		itemID, secret, ok := strings.Cut(rest, "_")
		if !ok || !model.ValidID(itemID) {
			return Credential{}, ErrInvalid
		}
		value.ItemID, rest = itemID, secret
	default:
		return Credential{}, ErrInvalid
	}
	secret, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(secret) != secretBytes {
		return Credential{}, ErrInvalid
	}
	return value, nil
}

// OperatorFile and ProxyFile are where the daemon keeps the operator's and
// wi web's credentials. They live in the data root: whoever can read it is the
// operator already, and a sandbox that does not mount it cannot use them.
func OperatorFile(dataRoot string) string {
	return filepath.Join(dataRoot, "credentials", "operator")
}
func ProxyFile(dataRoot string) string { return filepath.Join(dataRoot, "credentials", "proxy") }

// Read loads a credential file, which must hold one credential of kind.
func Read(path string, kind Kind) (Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Credential{}, err
	}
	value, err := Parse(strings.TrimSpace(string(data)))
	if err != nil || value.Kind != kind {
		return Credential{}, fmt.Errorf("%s does not hold a wi %s credential", path, kind)
	}
	return value, nil
}

// Write stores a credential readable by its owner only, replacing any file
// atomically so a reader never sees half of one.
func Write(path string, value Credential) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.WriteString(value.token + "\n"); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Ensure returns the credential in path, minting and writing one when the
// file is missing or does not hold a credential of kind.
func Ensure(path string, kind Kind) (Credential, error) {
	if value, err := Read(path, kind); err == nil {
		return value, nil
	}
	value, err := New(kind, "")
	if err != nil {
		return Credential{}, err
	}
	return value, Write(path, value)
}

// Source reads the credential in path each time it is called, so a client
// keeps working when the daemon writes or rotates it after the client starts.
func Source(path string, kind Kind) func() (string, error) {
	return func() (string, error) {
		value, err := Read(path, kind)
		return value.token, err
	}
}

// ForProcess is the credential a wi client presents: the item credential its
// environment names when it runs as an item's agent, otherwise the
// operator's. An agent never falls back to the operator's credential, even
// when its own has stopped working.
func ForProcess(env map[string]string, dataRoot string) func() (string, error) {
	if path := strings.TrimSpace(env[FileVariable]); path != "" {
		return Source(path, Item)
	}
	return Source(OperatorFile(dataRoot), Operator)
}
