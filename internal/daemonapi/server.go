// Package daemonapi translates wi.core.v1 to daemon-owned application use cases.
package daemonapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/regb/workitem/internal/apperr"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/gen/wi/core/v1/corev1connect"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const ClientHeader = "Wi-Client-Id"
const HolderHeader = "Wi-Client-Kind"

// Server owns use-case composition, operation serialization and background jobs.
// All structured writes still pass through the coordinator's database.
type Server struct {
	App             *app.App
	Core            *coordinator.Server
	Local           *coordinator.Client
	Config          config.Config
	Plugins         *plugin.Host
	Root            string
	StartedAt       time.Time
	ctx             context.Context
	cancel          context.CancelFunc
	locks           sync.Map
	jobs            *jobRegistry
	leases          sync.Map
	planSecret      [32]byte
	credentials     credentials
	callbackMu      sync.Mutex
	callbacksClosed bool
	callbacks       sync.WaitGroup
	usage           usageState
	// watchers counts open Watch and WatchChanges streams; stopBeat ends
	// the observation beat they keep running.
	watchMu  sync.Mutex
	watchers int
	stopBeat context.CancelFunc
}

func New(ctx context.Context, core *coordinator.Server, a *app.App, cfg config.Config, host *plugin.Host, root string) (*Server, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{App: a, Core: core, Local: core.LocalClient(), Config: cfg, Plugins: host, Root: root, StartedAt: time.Now().UTC(), ctx: ctx, cancel: cancel}
	if _, err := rand.Read(s.planSecret[:]); err != nil {
		cancel()
		return nil, err
	}
	var err error
	if s.credentials, err = loadCredentials(root); err != nil {
		cancel()
		return nil, err
	}
	a.ItemCredentials = core.Database()
	s.jobs, err = newPersistentJobRegistry(ctx, core.Database())
	if err != nil {
		cancel()
		return nil, err
	}
	s.jobs.itemChanged = func(itemID string) { core.Database().NotifyItems(itemID) }
	core.SetRuntimeSettingsObserver(s.observeRuntimeSettings)
	s.callbacks.Add(1)
	go s.scheduleLoop()
	s.callbacks.Add(1)
	go s.usageLoop()
	s.callbacks.Add(1)
	go s.blockerLoop()
	a.ReviewRecords = core
	a.ReviewCaptures = app.NewReviewCaptureQueue(ctx, a.CaptureAfterSettlement)
	a.ReviewCaptures.Changed = core.Database().NotifyAll
	core.SetRuntimeSettlementObserver(a.ReviewCaptures.Trigger)
	return s, nil
}
func (s *Server) Close() {
	s.callbackMu.Lock()
	s.callbacksClosed = true
	s.cancel()
	s.callbackMu.Unlock()
	s.App.ReviewCaptures.Close()
	s.jobs.wait()
	s.callbacks.Wait()
}
func (s *Server) observeRuntimeSettings(itemID, runtimeID string, settings map[string]string, change bool) {
	s.callbackMu.Lock()
	if s.callbacksClosed {
		s.callbackMu.Unlock()
		return
	}
	s.callbacks.Add(1)
	s.callbackMu.Unlock()
	go func() {
		defer s.callbacks.Done()
		ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
		defer cancel()
		unlock := s.lock(itemID)
		defer unlock()
		_ = s.App.AdoptRuntimeSettings(ctx, itemID, runtimeID, settings, change)
	}()
}
func (s *Server) lock(id string) func() {
	value, _ := s.locks.LoadOrStore(id, new(sync.Mutex))
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// identity is who a call is from: the principal its credential proves, the
// client and its kind, and, from wi web, the signed-in user it vouches for
// (label).
type identity struct {
	principal             principal
	client, holder, label string
}

// LabelHeader carries the user wi web's proxy authenticated, such as a
// Tailscale login. It is believed only with wi web's credential: wi web sets
// it and drops whatever the page sends.
const LabelHeader = "Wi-Client-Label"

// webLabel is a web caller's user, when wi web sent a valid one.
func webLabel(who principal, header http.Header) string {
	label := strings.TrimSpace(header.Get(LabelHeader))
	if !who.web() || len(label) > 128 || !utf8.ValidString(label) || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return ""
	}
	return label
}

// callerIdentity reads who a call is from, once its credential is checked.
func callerIdentity(who principal, header http.Header) identity {
	value := identity{principal: who, client: header.Get(ClientHeader), holder: header.Get(HolderHeader), label: webLabel(who, header)}
	if value.client == "" {
		value.client = "cli"
	}
	if value.holder != model.LeaseHolderTerminal && value.holder != model.LeaseHolderCLI {
		value.holder = model.LeaseHolderWeb
	}
	return value
}

// controlActor is who a control command is from: the caller's principal.
func controlActor(ctx context.Context) string { return model.ActorFrom(ctx) }

type identityKey struct{}

func caller(ctx context.Context) identity { v, _ := ctx.Value(identityKey{}).(identity); return v }

func (s *Server) unaryInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			who, err := s.authenticate(req.Header())
			if err != nil {
				return nil, err
			}
			s.noteActivity()
			caller := callerIdentity(who, req.Header())
			ctx = context.WithValue(ctx, identityKey{}, caller)
			ctx = model.WithOrigin(ctx, model.Origin{Actor: who.String()})
			if who.web() && !webAllowed(req.Spec().Procedure) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the web endpoint does not allow this operation"))
			}
			method := req.Spec().Procedure[strings.LastIndex(req.Spec().Procedure, "/")+1:]
			readOnly := strings.HasPrefix(method, "Get") || strings.HasPrefix(method, "List") || strings.HasPrefix(method, "Search") || strings.HasPrefix(method, "Resolve") || method == "SuggestTitle"
			if msg, ok := req.Any().(proto.Message); ok {
				ref := msg.ProtoReflect()
				if repoField := ref.Descriptor().Fields().ByName("repository_id"); repoField != nil {
					repositoryID := ref.Get(repoField).String()
					if !model.ValidID(repositoryID) {
						return nil, invalid("valid repository_id is required")
					}
					if !readOnly {
						unlock := s.lock("repository:" + repositoryID)
						defer unlock()
					}
				}
				field := ref.Descriptor().Fields().ByName("item_id")
				// ReleaseControl names its item by lease_id or item_id.
				optional := field != nil && req.Spec().Procedure == corev1connect.ControlServiceReleaseControlProcedure && ref.Get(field).String() == ""
				if field != nil && field.Kind() == protoreflect.StringKind && !strings.Contains(req.Spec().Procedure, "/ListJobs") && !optional {
					id := ref.Get(field).String()
					if !model.ValidID(id) {
						return nil, invalid("valid item_id is required")
					}
					if !readOnly && !strings.HasPrefix(req.Spec().Procedure, "/wi.core.v1.ReviewService/") {
						unlock := s.lock(id)
						defer unlock()
					}
				}
			}
			if !readOnly && strings.HasPrefix(req.Spec().Procedure, "/wi.core.v1.AgentProfileService/") {
				unlock := s.lock(":profiles")
				defer unlock()
			}
			result, err := next(ctx, req)
			if storeErr := s.jobs.healthError(); storeErr != nil {
				return nil, connect.NewError(connect.CodeUnavailable, storeErr)
			}
			return result, rpcError(err)
		}
	}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	opts := []connect.HandlerOption{connect.WithCodec(apitransport.JSONCodec{}), connect.WithReadMaxBytes(32 << 20), connect.WithInterceptors(apiInterceptor{s})}
	mux.Handle(corev1connect.NewItemServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewLifecycleServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewWorkspaceServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewReviewServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewAgentServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewControlServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewRepositoryServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewAgentProfileServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewScheduleServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewJobServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewEventServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewSystemServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewViewServiceHandler(s, opts...))
	return mux
}
func invalid(message string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(message))
}
func precondition(message string) error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(message))
}
func aborted(message string) error { return connect.NewError(connect.CodeAborted, errors.New(message)) }
func rpcError(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return connect.NewError(connect.CodeNotFound, err)
	}
	var ambiguous store.AmbiguousError
	if errors.As(err, &ambiguous) {
		value := connect.NewError(connect.CodeInvalidArgument, err)
		value.Meta().Set("Wi-Error-Reason", "ambiguous")
		return value
	}
	message := err.Error()
	if strings.Contains(message, "Use --force to activate this work item anyway") {
		message = strings.ReplaceAll(message, "Use --force to activate this work item anyway", "Free deep-work capacity before retrying")
		err = errors.New(message)
	}
	code := connect.CodeFailedPrecondition
	// An error's kind decides its code. Matching words remains only for
	// errors not yet given a kind, or whose kind a %v wrap lost: two exact
	// stale phrases, but no longer "generation", which also named invalid
	// requests, telling clients to retry what would fail again.
	switch {
	case errors.Is(err, apperr.Exhausted):
		code = connect.CodeResourceExhausted
	case errors.Is(err, apperr.Stale):
		code = connect.CodeAborted
	case errors.Is(err, apperr.NotFound):
		code = connect.CodeNotFound
	case errors.Is(err, apperr.Exists):
		code = connect.CodeAlreadyExists
	case errors.Is(err, apperr.Invalid):
		code = connect.CodeInvalidArgument
	case strings.Contains(message, "exhausted"):
		code = connect.CodeResourceExhausted
	case strings.Contains(message, "revision conflict"), strings.Contains(message, "changed since"):
		code = connect.CodeAborted
	case strings.Contains(message, "not found"), strings.Contains(message, "no such"), strings.Contains(message, "is not registered"), strings.Contains(message, "no agent profile"), strings.Contains(message, "does not exist"):
		code = connect.CodeNotFound
	case strings.Contains(message, "already exists"), strings.Contains(message, "collision"), strings.Contains(message, "already used"):
		code = connect.CodeAlreadyExists
	case strings.Contains(message, "invalid"), strings.Contains(message, "required"), strings.Contains(message, "must be"), strings.Contains(message, "unknown state"):
		code = connect.CodeInvalidArgument
	}
	return connect.NewError(code, err)
}
func response[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, rpcError(err)
	}
	return connect.NewResponse(msg), nil
}
func stamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
func optionalStamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return stamp(*t)
}
func id() string { value, _ := model.NewID(); return value }

// resolve names an item by its ID. The interceptor has refused any item_id
// that is not a whole ID, and a whole ID never falls back to matching slugs,
// titles or labels, so this resolves exactly that item or none. Names are
// resolved only by ResolveItem.
func resolve(itemID string) app.ResolveOptions { return app.ResolveOptions{Selector: itemID} }
func state(value string) v1.ItemState {
	return v1.ItemState(v1.ItemState_value["ITEM_STATE_"+strings.ToUpper(value)])
}
func stateName(value v1.ItemState) string {
	return strings.ToLower(strings.TrimPrefix(value.String(), "ITEM_STATE_"))
}
func resolution(value string) v1.Resolution {
	return v1.Resolution(v1.Resolution_value["RESOLUTION_"+strings.ToUpper(value)])
}
func resolutionName(value v1.Resolution) string {
	if value == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(value.String(), "RESOLUTION_"))
}
func mode(value v1.AgentMode) string {
	if value == v1.AgentMode_AGENT_MODE_TUI {
		return "tui"
	}
	return "headless"
}
func modeValue(value string) v1.AgentMode {
	if value == "tui" {
		return v1.AgentMode_AGENT_MODE_TUI
	}
	if value == "headless" {
		return v1.AgentMode_AGENT_MODE_HEADLESS
	}
	return 0
}
func agentStatus(value string) v1.AgentStatus {
	return v1.AgentStatus(v1.AgentStatus_value["AGENT_STATUS_"+strings.ToUpper(value)])
}
func worktreeStatus(value string) v1.WorktreeStatus {
	return v1.WorktreeStatus(v1.WorktreeStatus_value["WORKTREE_STATUS_"+strings.ToUpper(value)])
}
func (s *Server) canonical(ctx context.Context, itemID string, expected uint32) (coordinator.CanonicalManifest, error) {
	value, err := s.Local.CanonicalManifest(ctx, itemID)
	if err == nil && value.Revision > uint64(^uint32(0)) {
		return value, connect.NewError(connect.CodeResourceExhausted, errors.New("API item revision exhausted"))
	}
	if err == nil && expected != 0 && uint64(expected) != value.Revision {
		err = aborted(fmt.Sprintf("item revision is %d, expected %d", value.Revision, expected))
	}
	return value, err
}
func (s *Server) item(ctx context.Context, itemID string) (*v1.Item, error) {
	c, err := s.canonical(ctx, itemID, 0)
	if err != nil {
		return nil, err
	}
	return item(c.Manifest, c.Revision), nil
}
func checkout(m model.Checkout) *v1.Checkout {
	result := &v1.Checkout{Branch: m.Specification.Branch, Kind: m.Specification.Kind, Executor: m.Specification.Executor}
	if c := m.Claim; c != nil {
		result.Claim = &v1.WorkspaceClaim{Id: c.ID, Generation: uint32(c.Generation), Path: c.Path, Slot: c.Slot}
	}
	return result
}
func blockers(values []model.Blocker) []*v1.Blocker {
	out := []*v1.Blocker{}
	for _, b := range values {
		out = append(out, &v1.Blocker{Id: b.ID, Kind: v1.BlockerKind(v1.BlockerKind_value["BLOCKER_KIND_"+strings.ToUpper(b.Kind)]), Until: optionalStamp(b.Until), ItemId: b.ItemID, Event: b.Event, Label: b.Label, Meta: b.Meta, CreatedAt: stamp(b.CreatedAt), Provider: b.Provider, FollowUp: b.FollowUp})
	}
	return out
}
func item(m model.Manifest, revision uint64) *v1.Item {
	result := &v1.Item{Id: m.ID, Slug: m.Slug, Title: m.Title, State: state(m.State), Resolution: resolution(m.Resolution), Labels: m.Labels, DeepWork: m.DeepWork, Repository: &v1.RepositoryRef{Id: m.Repository.ID, Name: m.Repository.Name}, CreatedFromCommit: m.Repository.CreatedFromCommit, Checkout: checkout(m.Checkout), AgentProfile: m.Agent, AgentSettings: m.AgentSettings, Blockers: blockers(m.Blockers), CreatedAt: stamp(m.CreatedAt), UpdatedAt: stamp(m.UpdatedAt), StateChangedAt: stamp(m.StateChangedAt), Revision: uint32(revision), Priority: m.Priority, Pinned: m.Pinned, SnoozedUntil: optionalStamp(m.SnoozedUntil)}
	if m.PrimaryConversation != nil {
		result.Conversation = &v1.ConversationRef{Id: m.PrimaryConversation.ID, Driver: m.PrimaryConversation.Driver}
	}
	for _, f := range m.FollowUps {
		result.FollowUps = append(result.FollowUps, &v1.FollowUp{Id: f.ID, Message: f.Message, BlockedAt: stamp(f.BlockedAt), ClearedAt: stamp(f.ClearedAt)})
	}
	return result
}
