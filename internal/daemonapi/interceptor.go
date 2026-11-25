package daemonapi

import (
	"connectrpc.com/connect"
	"context"
	"errors"
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
	s := i.s
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		principal, err := s.authenticate(conn.RequestHeader())
		if err != nil {
			return err
		}
		if principal.web() && !webAllowed(conn.Spec().Procedure) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("the web endpoint does not allow this stream"))
		}
		who := callerIdentity(principal, conn.RequestHeader())
		ctx = model.WithOrigin(context.WithValue(ctx, identityKey{}, who), model.Origin{Actor: principal.String()})
		return rpcError(next(ctx, conn))
	}
}
