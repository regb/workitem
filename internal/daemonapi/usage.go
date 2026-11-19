package daemonapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/usage"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// usageInterval matches the usage plugin's own cache lifetime.
	usageInterval = 5 * time.Minute
	// usageIdle stops background checks once nobody has used the API for
	// this long; the next GetUsage starts one again.
	usageIdle = 30 * time.Minute
	// usageTick is how often the daemon considers a background check.
	usageTick = 30 * time.Second
	// usageTimeout bounds one run of the usage plugin.
	usageTimeout = 2 * time.Minute
)

// usageState is the daemon's last usage result and the check in progress.
// lastActivity is the Unix time of the latest unary API call.
type usageState struct {
	mu           sync.Mutex
	result       *v1.GetUsageResponse
	checkedAt    time.Time
	running      chan struct{}
	lastActivity atomic.Int64
	// run checks usage once; tests replace it.
	run func(ctx context.Context, refresh bool) (*v1.GetUsageResponse, error)
}

// noteActivity records an API call; background checks run only while the
// API is in use.
func (s *Server) noteActivity() { s.usage.lastActivity.Store(time.Now().Unix()) }

func (s *Server) GetUsage(ctx context.Context, req *connect.Request[v1.GetUsageRequest]) (*connect.Response[v1.GetUsageResponse], error) {
	u := &s.usage
	u.mu.Lock()
	first := u.result == nil
	var done chan struct{}
	if req.Msg.Refresh || first {
		done = s.startUsageCheck(req.Msg.Refresh)
	}
	u.mu.Unlock()
	if done != nil && (req.Msg.Wait || first) {
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return connect.NewResponse(s.usageSnapshot()), nil
}

// usageSnapshot copies the stored result with the current check status.
func (s *Server) usageSnapshot() *v1.GetUsageResponse {
	u := &s.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	out := &v1.GetUsageResponse{Quotas: []*v1.UsageQuota{}}
	if u.result != nil {
		out.Enabled, out.Quotas, out.Error = u.result.Enabled, u.result.Quotas, u.result.Error
		out.CheckedAt = timestamppb.New(u.checkedAt)
	}
	out.Refreshing = u.running != nil
	return out
}

// startUsageCheck starts a check unless one is running, and returns the
// channel closed when the running check ends. The caller holds u.mu.
func (s *Server) startUsageCheck(refresh bool) chan struct{} {
	u := &s.usage
	if u.running != nil {
		return u.running
	}
	if !s.startCallback() {
		return nil
	}
	done := make(chan struct{})
	u.running = done
	go func() {
		defer s.callbacks.Done()
		ctx, cancel := context.WithTimeout(s.ctx, usageTimeout)
		defer cancel()
		run := u.run
		if run == nil {
			run = s.checkUsage
		}
		result, err := run(ctx, refresh)
		u.mu.Lock()
		if err != nil {
			// Keep the previous quotas, and say why they were not renewed.
			kept := &v1.GetUsageResponse{Quotas: []*v1.UsageQuota{}, Error: err.Error()}
			if u.result != nil {
				kept.Enabled, kept.Quotas = u.result.Enabled, u.result.Quotas
			}
			u.result = kept
		} else {
			u.result = result
		}
		u.checkedAt = time.Now().UTC()
		u.running = nil
		u.mu.Unlock()
		close(done)
	}()
	return done
}

// usageLoop checks usage every usageInterval while the API is in use.
func (s *Server) usageLoop() {
	defer s.callbacks.Done()
	ticker := time.NewTicker(usageTick)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.maybeCheckUsage(time.Now())
		}
	}
}

// maybeCheckUsage starts a background check when the last one is older than
// usageInterval and the API was used within usageIdle.
func (s *Server) maybeCheckUsage(now time.Time) {
	u := &s.usage
	if now.Sub(time.Unix(u.lastActivity.Load(), 0)) > usageIdle {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.result != nil && now.Sub(u.checkedAt) < usageInterval {
		return
	}
	s.startUsageCheck(false)
}

// checkUsage runs the enabled usage plugin, as `wi usage --json` does.
func (s *Server) checkUsage(ctx context.Context, refresh bool) (*v1.GetUsageResponse, error) {
	out := &v1.GetUsageResponse{Quotas: []*v1.UsageQuota{}}
	if s.Plugins == nil {
		return out, nil
	}
	specs, err := s.Plugins.Specs(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, spec := range specs {
		if spec.Name != "usage" || !spec.Enabled {
			continue
		}
		out.Enabled = true
		args := []string{"--json", "show"}
		if refresh {
			args = append(args, "--refresh")
		}
		env := []string{}
		for _, entry := range os.Environ() {
			if key, _, _ := strings.Cut(entry, "="); !strings.HasPrefix(key, "WI_PLUGIN") && key != "WI_EXECUTABLE" {
				env = append(env, entry)
			}
		}
		env = append(env, plugin.Variables(spec, s.Plugins.SelfPath)...)
		var stdout, stderr bytes.Buffer
		if err := plugin.RunCommand(ctx, spec, args, env, "", nil, &stdout, &stderr); err != nil {
			return nil, fmt.Errorf("the usage plugin failed: %s", strings.TrimSpace(stderr.String()))
		}
		var snapshot usage.Snapshot
		if err := json.Unmarshal(stdout.Bytes(), &snapshot); err != nil {
			return nil, fmt.Errorf("the usage plugin returned unreadable results")
		}
		for _, result := range snapshot.Results {
			out.Quotas = append(out.Quotas, usageQuota(result))
		}
	}
	return out, nil
}

func usageQuota(r usage.QuotaResult) *v1.UsageQuota {
	out := &v1.UsageQuota{Provider: r.Provider, Account: r.Account, IncludedUsageAllowed: r.IncludedUsageAllowed, Stale: r.Stale, Windows: []*v1.UsageWindow{}}
	if r.ObservedAt != nil {
		out.ObservedAt = timestamppb.New(*r.ObservedAt)
	}
	for _, w := range r.Windows {
		window := &v1.UsageWindow{Id: w.ID, RemainingPercent: w.RemainingPercent, DurationSeconds: w.DurationSeconds}
		if w.Scope != nil {
			window.ScopeModel, window.ScopeSurface = w.Scope.Model, w.Scope.Surface
		}
		if w.ResetsAt != nil {
			window.ResetsAt = timestamppb.New(*w.ResetsAt)
		}
		out.Windows = append(out.Windows, window)
	}
	if r.Credits != nil {
		out.Credits = &v1.UsageCredits{Remaining: r.Credits.Remaining, Unlimited: r.Credits.Unlimited, Unit: r.Credits.Unit}
	}
	if r.Error != nil {
		out.ErrorCode, out.ErrorMessage = string(r.Error.Code), r.Error.Message
	}
	return out
}
