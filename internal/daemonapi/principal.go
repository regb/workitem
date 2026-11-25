package daemonapi

import (
	"context"
	"errors"
	"fmt"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"net/http"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/model"
)

// principal is who a call is from, as the credential it presented proves.
// The operator and wi web (acting for its signed-in user) are the local
// user; an item credential is that item's agent.
type principal struct {
	kind credential.Kind
	// itemID is the item whose agent calls, for item credentials.
	itemID string
}

// OperatorUser is the one user an installation has.
const OperatorUser = model.OperatorUser

// String is how records and clients name the principal.
func (p principal) String() string {
	if p.kind == credential.Item {
		return model.ItemPrincipal(p.itemID)
	}
	return OperatorUser
}

// web reports wi web's proxy, the only caller trusted to name its user and
// held to the web exposure table.
func (p principal) web() bool { return p.kind == credential.Proxy }

// item reports an item's agent.
func (p principal) item() bool { return p.kind == credential.Item }

// credentials holds the hashes of the daemon's operator and proxy
// credentials. Item credentials are checked against the database.
type credentials struct{ operator, proxy [32]byte }

// rotateCredentials writes new operator and wi web credentials and returns
// their hashes; the caller swaps them in.
func rotateCredentials(dataRoot string) (credentials, error) {
	operator, err := credential.New(credential.Operator, "")
	if err != nil {
		return credentials{}, err
	}
	proxy, err := credential.New(credential.Proxy, "")
	if err != nil {
		return credentials{}, err
	}
	if err := credential.Write(credential.OperatorFile(dataRoot), operator); err != nil {
		return credentials{}, err
	}
	if err := credential.Write(credential.ProxyFile(dataRoot), proxy); err != nil {
		return credentials{}, err
	}
	return credentials{operator: operator.Hash(), proxy: proxy.Hash()}, nil
}

// loadCredentials makes sure the operator's and wi web's credential files
// exist in the data root, and keeps their hashes.
func loadCredentials(dataRoot string) (credentials, error) {
	operator, err := credential.Ensure(credential.OperatorFile(dataRoot), credential.Operator)
	if err != nil {
		return credentials{}, fmt.Errorf("operator credential: %w", err)
	}
	proxy, err := credential.Ensure(credential.ProxyFile(dataRoot), credential.Proxy)
	if err != nil {
		return credentials{}, fmt.Errorf("web credential: %w", err)
	}
	// Only the daemon's user may read them; a directory left open by an
	// older wi or by hand is closed again.
	if err := os.Chmod(filepath.Dir(credential.OperatorFile(dataRoot)), 0o700); err != nil {
		return credentials{}, err
	}
	return credentials{operator: operator.Hash(), proxy: proxy.Hash()}, nil
}

var errUnauthenticated = errors.New("a valid wi credential is required")

// authenticate resolves the credential a request presents. A missing,
// unknown or stale credential is refused: an agent whose runtime ended does
// not quietly become the operator.
func (s *Server) authenticate(header http.Header) (principal, error) {
	token := apitransport.Bearer(header)
	if token == "" {
		return principal{}, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	value, err := credential.Parse(token)
	if err != nil {
		return principal{}, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	known := s.knownCredentials()
	switch value.Kind {
	case credential.Operator:
		if value.Matches(known.operator) {
			return principal{kind: credential.Operator}, nil
		}
	case credential.Proxy:
		if value.Matches(known.proxy) {
			return principal{kind: credential.Proxy}, nil
		}
	case credential.Item:
		valid, err := s.Core.Database().VerifyItemCredential(value)
		if err != nil {
			return principal{}, connect.NewError(connect.CodeUnavailable, err)
		}
		if valid {
			return principal{kind: credential.Item, itemID: value.ItemID}, nil
		}
		return principal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("this agent's wi credential is no longer valid: its runtime has stopped or was replaced"))
	}
	return principal{}, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
}

func (s *Server) knownCredentials() credentials {
	s.credentialsMu.RLock()
	defer s.credentialsMu.RUnlock()
	return s.credentials
}

func (s *Server) RotateCredentials(ctx context.Context, req *connect.Request[v1.RotateCredentialsRequest]) (*connect.Response[v1.RotateCredentialsResponse], error) {
	s.credentialsMu.Lock()
	defer s.credentialsMu.Unlock()
	next, err := rotateCredentials(s.Root)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.credentials = next
	return connect.NewResponse(&v1.RotateCredentialsResponse{}), nil
}
