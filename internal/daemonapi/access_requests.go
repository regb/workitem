package daemonapi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

// An item's agent refused for want of a level on an item asks, in effect,
// for that level: the daemon remembers the request so a person can grant it
// where they see it, or dismiss it. Requests live in memory, bounded; a
// granted one goes once the agent has the level.

// maxAccessRequests bounds the requests remembered.
const maxAccessRequests = 500

type accessRequestKey struct{ subject, itemID string }

type accessRequest struct {
	needed, has model.AccessLevel
	procedure   string
	at          time.Time
	refusals    uint32
}

type accessRequests struct {
	mu       sync.Mutex
	requests map[accessRequestKey]*accessRequest
}

// record remembers a refusal, keeping the highest level needed.
func (r *accessRequests) record(subject, itemID, procedure string, needed, has model.AccessLevel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requests == nil {
		r.requests = map[accessRequestKey]*accessRequest{}
	}
	key := accessRequestKey{subject, itemID}
	current := r.requests[key]
	if current == nil {
		current = &accessRequest{}
		r.requests[key] = current
	}
	if needed > current.needed {
		current.needed = needed
	}
	current.has, current.procedure, current.at = has, procedure, time.Now().UTC()
	current.refusals++
	for len(r.requests) > maxAccessRequests {
		var oldest accessRequestKey
		first := true
		for k, v := range r.requests {
			if first || v.at.Before(r.requests[oldest].at) {
				oldest, first = k, false
			}
		}
		delete(r.requests, oldest)
	}
}

func (r *accessRequests) forget(subject, itemID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := accessRequestKey{subject, itemID}
	_, found := r.requests[key]
	delete(r.requests, key)
	return found
}

func (r *accessRequests) snapshot(subject string) map[accessRequestKey]accessRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[accessRequestKey]accessRequest{}
	for k, v := range r.requests {
		if subject == "" || k.subject == subject {
			out[k] = *v
		}
	}
	return out
}

func (s *Server) ListAccessRequests(ctx context.Context, req *connect.Request[v1.ListAccessRequestsRequest]) (*connect.Response[v1.ListAccessRequestsResponse], error) {
	subject := strings.TrimSpace(req.Msg.Subject)
	if subject != "" && model.PrincipalItem(subject) == "" {
		return nil, invalid("subject must be an item's agent, item:<id>")
	}
	out := &v1.ListAccessRequestsResponse{Requests: []*v1.AccessRequest{}}
	for key, request := range s.accessRequests.snapshot(subject) {
		// A request the agent's access now meets, or about an item gone, is
		// done.
		has, err := s.itemAccess(ctx, key.subject, key.itemID)
		if err != nil || has >= request.needed {
			s.accessRequests.forget(key.subject, key.itemID)
			continue
		}
		out.Requests = append(out.Requests, &v1.AccessRequest{Subject: key.subject, ItemId: key.itemID, Needed: v1.AccessLevel(request.needed), Has: v1.AccessLevel(has), Procedure: request.procedure, LastRefusedAt: stamp(request.at), Refusals: request.refusals})
	}
	sort.Slice(out.Requests, func(i, j int) bool {
		return out.Requests[i].LastRefusedAt.AsTime().After(out.Requests[j].LastRefusedAt.AsTime())
	})
	return response(out, nil)
}

func (s *Server) DismissAccessRequest(ctx context.Context, req *connect.Request[v1.DismissAccessRequestRequest]) (*connect.Response[v1.DismissAccessRequestResponse], error) {
	if !s.accessRequests.forget(strings.TrimSpace(req.Msg.Subject), strings.TrimSpace(req.Msg.ItemId)) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no access request of %s on %s", req.Msg.Subject, req.Msg.ItemId))
	}
	return response(&v1.DismissAccessRequestResponse{}, nil)
}
