// Package daemonapi translates wi.core.v1 to daemon-owned application use cases.
package daemonapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
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
	callbackMu      sync.Mutex
	callbacksClosed bool
	callbacks       sync.WaitGroup
}

func New(ctx context.Context, core *coordinator.Server, a *app.App, cfg config.Config, host *plugin.Host, root string) (*Server, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{App: a, Core: core, Local: core.LocalClient(), Config: cfg, Plugins: host, Root: root, StartedAt: time.Now().UTC(), ctx: ctx, cancel: cancel}
	if _, err := rand.Read(s.planSecret[:]); err != nil {
		cancel()
		return nil, err
	}
	var err error
	s.jobs, err = newPersistentJobRegistry(ctx, core.Database())
	if err != nil {
		cancel()
		return nil, err
	}
	core.SetRuntimeSettingsObserver(s.observeRuntimeSettings)
	return s, nil
}
func (s *Server) Close() {
	s.callbackMu.Lock()
	s.callbacksClosed = true
	s.cancel()
	s.callbackMu.Unlock()
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

type identity struct{ client, holder, actor string }

const ControlActorHeader = "Wi-Control-Actor"

func controlActor(ctx context.Context) string {
	if actor := caller(ctx).actor; actor != "" {
		return actor
	}
	return "operator"
}

type identityKey struct{}

func caller(ctx context.Context) identity { v, _ := ctx.Value(identityKey{}).(identity); return v }

func (s *Server) unaryInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if access := apitransport.Caller(ctx); access != apitransport.Operator && access != apitransport.Agent && access != apitransport.WebAdapter {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("authenticated daemon endpoint is required"))
			}
			who := identity{client: req.Header().Get(ClientHeader), holder: req.Header().Get(HolderHeader), actor: req.Header().Get(ControlActorHeader)}
			if len(who.actor) > 128 || !utf8.ValidString(who.actor) || strings.IndexFunc(who.actor, unicode.IsControl) >= 0 {
				return nil, invalid("invalid control actor")
			}
			if who.client == "" {
				who.client = "cli"
			}
			if who.holder != model.LeaseHolderTerminal && who.holder != model.LeaseHolderCLI {
				who.holder = model.LeaseHolderWeb
			}
			ctx = context.WithValue(ctx, identityKey{}, who)
			if apitransport.Caller(ctx) == apitransport.Agent && !agentAllowed(req.Spec().Procedure) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("agent endpoint does not allow this operation"))
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
				if field != nil && field.Kind() == protoreflect.StringKind && !strings.Contains(req.Spec().Procedure, "/ListJobs") {
					id := ref.Get(field).String()
					if !model.ValidID(id) {
						return nil, invalid("valid item_id is required")
					}
					if !readOnly {
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
func agentAllowed(procedure string) bool {
	switch procedure {
	case corev1connect.SystemServiceGetInfoProcedure, corev1connect.ItemServiceCreateItemProcedure, corev1connect.ItemServiceResolveItemProcedure, corev1connect.ItemServiceGetItemProcedure, corev1connect.ItemServiceListItemsProcedure, corev1connect.ItemServiceSearchItemsProcedure, corev1connect.ItemServiceAddNoteProcedure, corev1connect.ItemServiceAttachProcedure, corev1connect.ItemServiceListNotesProcedure, corev1connect.ItemServiceReadAttachmentProcedure, corev1connect.RepositoryServiceListRepositoriesProcedure, corev1connect.AgentProfileServiceListAgentProfilesProcedure:
		return true
	}
	return false
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	opts := []connect.HandlerOption{connect.WithCodec(apitransport.JSONCodec{}), connect.WithReadMaxBytes(32 << 20), connect.WithInterceptors(apiInterceptor{s})}
	mux.Handle(corev1connect.NewItemServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewLifecycleServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewWorkspaceServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewAgentServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewControlServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewRepositoryServiceHandler(s, opts...))
	mux.Handle(corev1connect.NewAgentProfileServiceHandler(s, opts...))
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
	switch {
	case strings.Contains(message, "exhausted"):
		code = connect.CodeResourceExhausted
	case strings.Contains(message, "revision conflict"), strings.Contains(message, "changed since"), strings.Contains(message, "generation"):
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
func id() string                               { value, _ := model.NewID(); return value }
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
		out = append(out, &v1.Blocker{Id: b.ID, Kind: v1.BlockerKind(v1.BlockerKind_value["BLOCKER_KIND_"+strings.ToUpper(b.Kind)]), Until: optionalStamp(b.Until), ItemId: b.ItemID, Event: b.Event, Label: b.Label, Meta: b.Meta, CreatedAt: stamp(b.CreatedAt)})
	}
	return out
}
func item(m model.Manifest, revision uint64) *v1.Item {
	result := &v1.Item{Id: m.ID, Slug: m.Slug, Title: m.Title, State: state(m.State), Resolution: resolution(m.Resolution), Labels: m.Labels, DeepWork: m.DeepWork, Repository: &v1.RepositoryRef{Id: m.Repository.ID, Name: m.Repository.Name}, CreatedFromCommit: m.Repository.CreatedFromCommit, Checkout: checkout(m.Checkout), AgentProfile: m.Agent, AgentSettings: m.AgentSettings, Blockers: blockers(m.Blockers), CreatedAt: stamp(m.CreatedAt), UpdatedAt: stamp(m.UpdatedAt), StateChangedAt: stamp(m.StateChangedAt), Revision: uint32(revision), Priority: m.Priority, Pinned: m.Pinned, SnoozedUntil: optionalStamp(m.SnoozedUntil)}
	if m.PrimaryConversation != nil {
		result.Conversation = &v1.ConversationRef{Id: m.PrimaryConversation.ID, Driver: m.PrimaryConversation.Driver}
	}
	return result
}
