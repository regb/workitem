package daemonapi

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/model"
)

type apiInterceptor struct{ s *Server }

func (i apiInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return i.s.unaryInterceptor()(next)
}
func (i apiInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (i apiInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if access := apitransport.Caller(ctx); access != apitransport.Operator && access != apitransport.Agent && access != apitransport.WebAdapter {
			return connect.NewError(connect.CodePermissionDenied, errors.New("authenticated daemon endpoint is required"))
		}
		if apitransport.Caller(ctx) == apitransport.Agent && !agentAllowed(conn.Spec().Procedure) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("agent endpoint does not allow this stream"))
		}
		who := identity{client: conn.RequestHeader().Get(ClientHeader), holder: conn.RequestHeader().Get(HolderHeader)}
		if who.client == "" {
			who.client = "cli"
		}
		if who.holder != model.LeaseHolderTerminal && who.holder != model.LeaseHolderCLI {
			who.holder = model.LeaseHolderWeb
		}
		return rpcError(next(context.WithValue(ctx, identityKey{}, who), conn))
	}
}
