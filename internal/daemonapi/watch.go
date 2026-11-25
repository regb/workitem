package daemonapi

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/gen/wi/core/v1/corev1connect"
	"google.golang.org/protobuf/proto"
)

// Watch timing: a heartbeat when nothing else was sent for heartbeatInterval;
// the observation beat while anyone watches.
var (
	heartbeatInterval   = 15 * time.Second
	observationInterval = 10 * time.Second
)

// Watch multiplexes what one client view follows onto one stream: changes,
// jobs, an item's live content and heartbeats. Each part runs on its own;
// the first to fail ends the stream, and the client reconnects.
func (s *Server) Watch(ctx context.Context, req *connect.Request[v1.WatchRequest], stream *connect.ServerStream[v1.WatchResponse]) error {
	if err := s.authorize(ctx, corev1connect.EventServiceWatchProcedure, requestItem(req.Msg)); err != nil {
		return err
	}
	if req.Msg.ItemId != "" {
		if _, err := s.canonical(ctx, req.Msg.ItemId, 0); err != nil {
			return rpcError(err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.watching()()
	var mu sync.Mutex
	var sent atomic.Int64
	send := func(out *v1.WatchResponse) error {
		mu.Lock()
		defer mu.Unlock()
		sent.Store(time.Now().UnixNano())
		return stream.Send(out)
	}
	done := make(chan error, 3)
	go func() {
		done <- s.followChanges(ctx, req.Msg.After, func(changes *v1.WatchChangesResponse) error {
			return send(&v1.WatchResponse{Event: &v1.WatchResponse_Changes{Changes: changes}})
		})
	}()
	go func() { done <- s.followJobs(ctx, send) }()
	if req.Msg.ItemId != "" {
		go func() {
			done <- s.followContent(ctx, req.Msg.ItemId, func(content *v1.WatchLiveContentResponse) error {
				return send(&v1.WatchResponse{Event: &v1.WatchResponse_Content{Content: content}})
			})
		}()
	}
	ticker := time.NewTicker(heartbeatInterval / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return err
		case <-ticker.C:
			if time.Since(time.Unix(0, sent.Load())) < heartbeatInterval {
				continue
			}
			if err := send(&v1.WatchResponse{Event: &v1.WatchResponse_Heartbeat{Heartbeat: &v1.Heartbeat{}}}); err != nil {
				return err
			}
		}
	}
}

func (s *Server) WatchChanges(ctx context.Context, req *connect.Request[v1.WatchChangesRequest], stream *connect.ServerStream[v1.WatchChangesResponse]) error {
	defer s.watching()()
	return s.followChanges(ctx, req.Msg.After, stream.Send)
}

// followChanges sends which items changed after a cursor, as WatchChanges
// documents, until ctx ends.
func (s *Server) followChanges(ctx context.Context, cursor string, send func(*v1.WatchChangesResponse) error) error {
	epoch := ""
	after := uint64(0)
	if data, err := base64.RawURLEncoding.DecodeString(cursor); err == nil {
		parts := strings.SplitN(string(data), ":", 2)
		if len(parts) == 2 {
			epoch = parts[0]
			after, _ = strconv.ParseUint(parts[1], 10, 64)
		}
	}
	first := true
	for {
		value, changed := s.Core.Database().ChangesSnapshot(epoch, after)
		if first || value.All || len(value.ItemIDs) > 0 {
			if first && cursor == "" {
				value.All = false
				value.ItemIDs = nil
			}
			out := &v1.WatchChangesResponse{Cursor: base64.RawURLEncoding.EncodeToString([]byte(value.Epoch + ":" + strconv.FormatUint(value.Revision, 10))), ItemIds: value.ItemIDs, All: value.All, ProfilesChanged: value.All}
			if err := send(out); err != nil {
				return err
			}
			epoch, after = value.Epoch, value.Revision
		}
		first = false
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// followJobs sends every unfinished job, then each job as it is created or
// changes state or phase, until ctx ends.
func (s *Server) followJobs(ctx context.Context, send func(*v1.WatchResponse) error) error {
	moved := s.jobs.movement()
	seen := map[string]*v1.Job{}
	running := s.jobs.list("", true)
	for _, job := range running {
		seen[job.Id] = job
	}
	if err := send(&v1.WatchResponse{Event: &v1.WatchResponse_Jobs{Jobs: &v1.JobsChanged{Jobs: running, Snapshot: true}}}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-moved:
		}
		moved = s.jobs.movement()
		var changed []*v1.Job
		for _, job := range s.jobs.list("", false) {
			before, known := seen[job.Id]
			// A job finished before this stream began is not news to it.
			if !known && terminalJob(job.State) && job.FinishedAt.AsTime().Before(time.Now().Add(-time.Minute)) {
				continue
			}
			if !known || before.State != job.State || before.Phase != job.Phase || !proto.Equal(before.Partial, job.Partial) {
				seen[job.Id] = job
				changed = append(changed, job)
			}
		}
		if len(changed) == 0 {
			continue
		}
		if err := send(&v1.WatchResponse{Event: &v1.WatchResponse_Jobs{Jobs: &v1.JobsChanged{Jobs: changed}}}); err != nil {
			return err
		}
	}
}

// watching counts a watcher; while there are any, the daemon re-observes
// agents and worktrees on the observation beat, and records changes for the
// items whose observation changed. The returned function ends the watch.
func (s *Server) watching() func() {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.watchers++
	if s.watchers == 1 {
		ctx, cancel := context.WithCancel(s.ctx)
		s.stopBeat = cancel
		s.callbackMu.Lock()
		if !s.callbacksClosed {
			s.callbacks.Add(1)
			go s.observationBeat(ctx)
		}
		s.callbackMu.Unlock()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.watchMu.Lock()
			defer s.watchMu.Unlock()
			s.watchers--
			if s.watchers == 0 {
				s.stopBeat()
			}
		})
	}
}

func (s *Server) observationBeat(ctx context.Context) {
	defer s.callbacks.Done()
	ticker := time.NewTicker(observationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A failed observation is retried on the next beat; readers keep
			// what they have meanwhile.
			_, _ = s.Local.ActivityBarrier(ctx)
		}
	}
}
