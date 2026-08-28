package coordinator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/version"
)

type Server struct {
	dataRoot        string
	socketPath      string
	database        *Database
	observer        *ExternalObserver
	native          *store.Store
	listener        *net.UnixListener
	agentListener   *net.UnixListener
	agentSocketPath string
	startedAt       time.Time
	buildIdentity   string
	domainMu        sync.Mutex
	notifyMu        sync.Mutex
	eventNotify     chan struct{}
	importMu        sync.RWMutex
	lastPiImport    PiImportReport
	closed          chan struct{}
	closeDone       chan struct{}
	closeOnce       sync.Once
	wg              sync.WaitGroup
}

func NewServer(dataRoot, socketPath string) (*Server, error) {
	return newServer(dataRoot, socketPath, "")
}

func NewServerWithAgentSocket(dataRoot, socketPath, agentSocketPath string) (*Server, error) {
	if strings.TrimSpace(agentSocketPath) == "" {
		return nil, errors.New("agent socket path is required")
	}
	return newServer(dataRoot, socketPath, agentSocketPath)
}

func newServer(dataRoot, socketPath, agentSocketPath string) (*Server, error) {
	if strings.TrimSpace(socketPath) == "" {
		return nil, errors.New("socket path is required")
	}
	if agentSocketPath != "" && filepath.Clean(agentSocketPath) == filepath.Clean(socketPath) {
		return nil, errors.New("operator and agent socket paths must differ")
	}
	database, err := OpenDatabase(dataRoot)
	if err != nil {
		return nil, err
	}
	nativeStore := store.New(dataRoot)
	if err := MaterializeNativeWrites(context.Background(), database, nativeStore); err != nil {
		database.Close()
		return nil, fmt.Errorf("materialize native writes: %w", err)
	}
	if err := CleanupOrphanedStages(database); err != nil {
		database.Close()
		return nil, fmt.Errorf("clean orphaned command staging: %w", err)
	}
	piReport, err := ImportPiSessions(context.Background(), database, dataRoot)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("index Pi sessions: %w", err)
	}
	observer := NewExternalObserver(database, dataRoot)
	if err := observer.Reconcile(context.Background()); err != nil {
		database.Close()
		return nil, fmt.Errorf("build external observations: %w", err)
	}
	if err := prepareSocketPath(socketPath); err != nil {
		database.Close()
		return nil, err
	}
	listener, err := listenUnixSocket(socketPath)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("listen on coordinator socket: %w", err)
	}
	var agentListener *net.UnixListener
	if agentSocketPath != "" {
		if err := prepareSocketPath(agentSocketPath); err != nil {
			listener.Close()
			database.Close()
			_ = os.Remove(socketPath)
			return nil, err
		}
		agentListener, err = listenUnixSocket(agentSocketPath)
		if err != nil {
			listener.Close()
			database.Close()
			_ = os.Remove(socketPath)
			return nil, fmt.Errorf("listen on agent coordinator socket: %w", err)
		}
	}
	server := &Server{dataRoot: dataRoot, socketPath: socketPath, agentSocketPath: agentSocketPath, database: database, observer: observer, native: nativeStore, listener: listener, agentListener: agentListener, startedAt: time.Now().UTC(), buildIdentity: version.BuildIdentity(), lastPiImport: piReport, eventNotify: make(chan struct{}), closed: make(chan struct{}), closeDone: make(chan struct{})}
	return server, nil
}

func listenUnixSocket(path string) (*net.UnixListener, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure coordinator socket: %w", err)
	}
	return listener, nil
}

func prepareSocketPath(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create coordinator runtime directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure coordinator runtime directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket coordinator path %s", path)
	}
	conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return fmt.Errorf("coordinator is already listening on %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale coordinator socket: %w", err)
	}
	return nil
}

func (s *Server) Serve(ctx context.Context) error {
	s.wg.Add(1)
	go s.reconcilePi(ctx)
	s.wg.Add(1)
	go s.reconcileExternal(ctx)
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.closed:
		}
	}()
	listenerCount := 1
	if s.agentListener != nil {
		listenerCount++
	}
	failures := make(chan error, listenerCount)
	go s.acceptConnections(s.listener, AccessOperator, failures)
	if s.agentListener != nil {
		go s.acceptConnections(s.agentListener, AccessAgent, failures)
	}
	select {
	case <-s.closed:
		<-s.closeDone
		return nil
	case err := <-failures:
		return err
	}
}

func (s *Server) acceptConnections(listener *net.UnixListener, access string, failures chan<- error) {
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				failures <- fmt.Errorf("accept %s coordinator connection: %w", access, err)
				return
			}
		}
		if err := validatePeerOwnership(conn); err != nil {
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn, access)
		}()
	}
}

func (s *Server) reconcilePi(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case <-ticker.C:
			s.domainMu.Lock()
			piReport, piErr := ImportPiSessions(ctx, s.database, s.dataRoot)
			s.domainMu.Unlock()
			if piErr == nil {
				s.importMu.Lock()
				s.lastPiImport = piReport
				s.importMu.Unlock()
			}
		}
	}
}

func (s *Server) reconcileExternal(ctx context.Context) {
	defer s.wg.Done()
	runtimeTicker := time.NewTicker(2 * time.Second)
	worktreeTicker := time.NewTicker(30 * time.Second)
	defer runtimeTicker.Stop()
	defer worktreeTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case <-runtimeTicker.C:
			s.domainMu.Lock()
			_ = s.observer.ReconcileRuntime(ctx)
			s.domainMu.Unlock()
		case <-worktreeTicker.C:
			s.domainMu.Lock()
			_ = s.observer.Reconcile(ctx)
			s.domainMu.Unlock()
		}
	}
}

func (s *Server) activityBarrier(ctx context.Context) (ActivityBarrierResult, error) {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	return s.refreshActionabilityLocked(ctx)
}

func (s *Server) refreshActionabilityLocked(ctx context.Context) (ActivityBarrierResult, error) {
	report, err := ImportActivePiSessions(ctx, s.database, s.dataRoot)
	if err != nil {
		return ActivityBarrierResult{}, err
	}
	if err := s.observer.Reconcile(ctx); err != nil {
		return ActivityBarrierResult{}, err
	}
	s.importMu.Lock()
	s.lastPiImport = report
	s.importMu.Unlock()
	status, err := s.database.Status()
	if err != nil {
		return ActivityBarrierResult{}, err
	}
	observedAt := time.Now().UTC()
	return ActivityBarrierResult{Projection: ProjectionMetadata{Revision: status.GlobalSequence, Source: "daemon.activity_barrier", ObservedAt: observedAt, Fresh: true, Warnings: append([]string(nil), report.Warnings...)}, PiImport: report}, nil
}

func (s *Server) actionabilitySnapshot(ctx context.Context, currentItemID string, options ActionabilityQueueOptions) (ActionabilitySnapshotResult, error) {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	barrier, err := s.refreshActionabilityLocked(ctx)
	if err != nil {
		return ActionabilitySnapshotResult{}, err
	}
	queue, err := s.database.ActionabilityQueue(options)
	if err != nil {
		return ActionabilitySnapshotResult{}, err
	}
	return ActionabilitySnapshotResult{Projection: barrier.Projection, PiImport: barrier.PiImport, Queue: queue, Selection: SelectActionability(queue, currentItemID)}, nil
}

func (s *Server) publishRuntimeEvent() {
	s.notifyMu.Lock()
	close(s.eventNotify)
	s.eventNotify = make(chan struct{})
	s.notifyMu.Unlock()
}

func (s *Server) runtimeEventWaiter() <-chan struct{} {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	return s.eventNotify
}

func (s *Server) Close() error {
	var result error
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.listener != nil {
			result = s.listener.Close()
		}
		if s.agentListener != nil {
			if err := s.agentListener.Close(); result == nil {
				result = err
			}
		}
		s.wg.Wait()
		if err := s.database.Close(); result == nil {
			result = err
		}
		_ = os.Remove(s.socketPath)
		if s.agentSocketPath != "" {
			_ = os.Remove(s.agentSocketPath)
			_ = os.Remove(filepath.Dir(s.agentSocketPath))
		}
		close(s.closeDone)
	})
	return result
}

func (s *Server) handle(conn *net.UnixConn, access string) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), MaxRequestBytes)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			s.writeError(conn, "", "request_too_large", fmt.Sprintf("request exceeds %d bytes", MaxRequestBytes))
		} else {
			s.writeError(conn, "", "invalid_request", "empty request")
		}
		return
	}
	var request Request
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
		s.writeError(conn, request.ID, "invalid_request", "decode request: "+err.Error())
		return
	}
	if request.ProtocolVersion != ProtocolVersion {
		s.writeError(conn, request.ID, "protocol_mismatch", fmt.Sprintf("unsupported protocol %d; expected %d", request.ProtocolVersion, ProtocolVersion))
		return
	}
	if request.BuildIdentity != "" && request.BuildIdentity != s.buildIdentity && request.Method != MethodStatus && request.Method != MethodShutdown && request.Method != MethodRuntimeEvent {
		s.writeError(conn, request.ID, "build_mismatch", "client and daemon builds differ; restart the older process")
		return
	}
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.Method) == "" {
		s.writeError(conn, request.ID, "invalid_request", "request id and method are required")
		return
	}
	if access == AccessAgent && !agentMethodAllowed(request.Method) {
		s.writeError(conn, request.ID, "permission_denied", "agent endpoint does not allow "+request.Method)
		return
	}
	var result any
	switch request.Method {
	case MethodPing:
		result = map[string]any{"pong": true}
	case MethodStatus:
		status, err := s.status()
		if err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		}
		status.Access = access
		if access == AccessAgent {
			status.DataRoot = ""
			status.SocketPath = s.agentSocketPath
			status.AgentSocketPath = ""
			status.Database.Path = ""
		}
		result = status
	case MethodShutdown:
		result = map[string]any{"stopping": true}
		defer func() { go s.Close() }()
	case MethodPiSession:
		var itemRequest ItemRequest
		if err := json.Unmarshal(request.Payload, &itemRequest); err != nil || !model.ValidID(itemRequest.ItemID) {
			s.writeError(conn, request.ID, "invalid_request", "valid item_id is required")
			return
		}
		var session PiSessionIndex
		found, err := s.database.ReadProjection(PiSessionProjection, itemRequest.ItemID, &session)
		if err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		}
		s.importMu.RLock()
		piWarnings := append([]string(nil), s.lastPiImport.Warnings...)
		s.importMu.RUnlock()
		result = PiSessionProjectionResult{Projection: ProjectionMetadata{Revision: uint64(session.Offset), Source: session.Source, ObservedAt: session.ObservedAt, Fresh: found, Warnings: piWarnings}, Found: found, Session: session}
	case MethodItemResources:
		var itemRequest ItemRequest
		if err := json.Unmarshal(request.Payload, &itemRequest); err != nil || !model.ValidID(itemRequest.ItemID) {
			s.writeError(conn, request.ID, "invalid_request", "valid item_id is required")
			return
		}
		resources := ItemResourcesResult{}
		var runtime model.AgentRuntime
		if found, err := s.database.ReadProjection(RuntimeOwnershipProjection, itemRequest.ItemID, &runtime); err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		} else if found {
			resources.Runtime = &runtime
		}
		var terminal model.TerminalRuntime
		if found, err := s.database.ReadProjection(TerminalOwnershipProjection, itemRequest.ItemID, &terminal); err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		} else if found {
			resources.Terminal = &terminal
		}
		result = resources
	case MethodAgentObservation:
		var itemRequest ItemRequest
		if err := json.Unmarshal(request.Payload, &itemRequest); err != nil || !model.ValidID(itemRequest.ItemID) {
			s.writeError(conn, request.ID, "invalid_request", "valid item_id is required")
			return
		}
		var observation AgentObservation
		found, err := s.database.ReadProjection(AgentObservationProjection, itemRequest.ItemID, &observation)
		if err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		}
		result = AgentObservationResult{Projection: ProjectionMetadata{Revision: uint64(observation.ObservedAt.UnixNano()), Source: "daemon.external_observer", ObservedAt: observation.ObservedAt, Fresh: found && time.Since(observation.ObservedAt) < 5*time.Second, Warnings: append([]string(nil), observation.Warnings...)}, Found: found, Observation: observation}
	case MethodCanonicalManifest:
		var itemRequest ItemRequest
		if err := json.Unmarshal(request.Payload, &itemRequest); err != nil || !model.ValidID(itemRequest.ItemID) {
			s.writeError(conn, request.ID, "invalid_request", "valid item_id is required")
			return
		}
		canonical, err := s.database.CanonicalManifest(itemRequest.ItemID)
		if err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		}
		result = canonical
	case MethodActivityBarrier:
		barrierCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		barrier, err := s.activityBarrier(barrierCtx)
		cancel()
		if err != nil {
			s.writeError(conn, request.ID, "barrier_error", err.Error())
			return
		}
		result = barrier
	case MethodActionability:
		var snapshotRequest ActionabilitySnapshotRequest
		if err := json.Unmarshal(request.Payload, &snapshotRequest); err != nil {
			s.writeError(conn, request.ID, "invalid_request", "decode actionability snapshot: "+err.Error())
			return
		}
		if snapshotRequest.CurrentItemID != "" && !model.ValidID(snapshotRequest.CurrentItemID) {
			s.writeError(conn, request.ID, "invalid_request", "current_item_id must be empty or valid")
			return
		}
		snapshotCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		snapshot, err := s.actionabilitySnapshot(snapshotCtx, snapshotRequest.CurrentItemID, snapshotRequest.Queue)
		cancel()
		if err != nil {
			s.writeError(conn, request.ID, "snapshot_error", err.Error())
			return
		}
		result = snapshot
	case MethodItemEvents:
		var itemRequest ItemRequest
		if err := json.Unmarshal(request.Payload, &itemRequest); err != nil || !model.ValidID(itemRequest.ItemID) {
			s.writeError(conn, request.ID, "invalid_request", "valid item_id is required")
			return
		}
		domainEvents, err := s.database.ListItemEvents(itemRequest.ItemID)
		if err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		}
		events := make([]model.Event, 0, len(domainEvents))
		for _, event := range domainEvents {
			events = append(events, event.toModelEvent())
		}
		result = ItemEventsResult{Events: events}
	case MethodRuntimeEvents:
		var eventsRequest RuntimeEventsRequest
		if err := json.Unmarshal(request.Payload, &eventsRequest); err != nil || !model.ValidID(eventsRequest.ItemID) || eventsRequest.Limit < 0 || eventsRequest.Limit > 10000 || eventsRequest.WaitMillis < 0 || eventsRequest.WaitMillis > 30000 {
			s.writeError(conn, request.ID, "invalid_request", "valid item_id, limit, and wait_millis are required")
			return
		}
		eventsResult, err := s.database.RuntimeEvents(eventsRequest.ItemID, eventsRequest.AfterSequence, eventsRequest.Limit)
		if err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		}
		if len(eventsResult.Events) == 0 && eventsRequest.WaitMillis > 0 {
			waiter := s.runtimeEventWaiter()
			timer := time.NewTimer(time.Duration(eventsRequest.WaitMillis) * time.Millisecond)
			select {
			case <-waiter:
			case <-timer.C:
			case <-s.closed:
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			eventsResult, err = s.database.RuntimeEvents(eventsRequest.ItemID, eventsRequest.AfterSequence, eventsRequest.Limit)
			if err != nil {
				s.writeError(conn, request.ID, "database_error", err.Error())
				return
			}
		}
		result = eventsResult
	case MethodRuntimeEvent:
		var runtimeEvent RuntimeSemanticEvent
		if err := json.Unmarshal(request.Payload, &runtimeEvent); err != nil {
			s.writeError(conn, request.ID, "invalid_request", "decode runtime event: "+err.Error())
			return
		}
		s.domainMu.Lock()
		runtimeResult, err := IngestRuntimeEvent(context.Background(), s.database, runtimeEvent)
		s.domainMu.Unlock()
		if err != nil {
			var ownerMismatch *RuntimeOwnerMismatchError
			if errors.As(err, &ownerMismatch) {
				s.writeError(conn, request.ID, "runtime_owner_mismatch", err.Error())
			} else {
				s.writeError(conn, request.ID, "runtime_event_error", err.Error())
			}
			return
		}
		if !runtimeResult.Duplicate {
			s.publishRuntimeEvent()
		}
		result = runtimeResult
	case MethodStoreCommand:
		var storeRequest StoreCommandRequest
		if err := json.Unmarshal(request.Payload, &storeRequest); err != nil {
			s.writeError(conn, request.ID, "invalid_request", "decode store command: "+err.Error())
			return
		}
		s.domainMu.Lock()
		err := StageStoreMutation(s.database, storeRequest.Command, storeRequest.Payload)
		var storeResult StoreCommandResult
		committed := false
		if err == nil {
			storeResult, err = s.database.ExecuteStoreCommand(storeRequest.Command, storeRequest.Payload)
			committed = err == nil
		}
		if committed {
			err = MaterializeNativeWrites(context.Background(), s.database, s.native)
		}
		if err == nil || !committed {
			RemoveStagedStoreMutation(s.database, storeRequest.Command.ID)
		}
		s.domainMu.Unlock()
		if err != nil {
			s.writeError(conn, request.ID, "command_error", err.Error())
			return
		}
		result = storeResult
	case MethodCreateItem:
		var createRequest CreateItemRequest
		if err := json.Unmarshal(request.Payload, &createRequest); err != nil {
			s.writeError(conn, request.ID, "invalid_request", "decode create command: "+err.Error())
			return
		}
		if access == AccessAgent {
			createRequest.Command.Actor = "agent"
			if err := validateAgentCreateItem(createRequest.Command.Manifest); err != nil {
				s.writeError(conn, request.ID, "permission_denied", err.Error())
				return
			}
		}
		if err := validateCreateDescription(createRequest.Command, createRequest.Description); err != nil {
			s.writeError(conn, request.ID, "invalid_request", err.Error())
			return
		}
		s.domainMu.Lock()
		err := StageDescription(s.database, createRequest.Command.ID, createRequest.Description)
		var createResult CreateItemResult
		committed := false
		if err == nil {
			if access == AccessAgent {
				createResult, err = s.database.ExecuteAgentCreateItemCommand(createRequest.Command)
			} else {
				createResult, err = s.database.ExecuteCreateItemCommand(createRequest.Command)
			}
			committed = err == nil
		}
		if committed {
			err = MaterializeNativeWrites(context.Background(), s.database, s.native)
		}
		if err == nil || !committed {
			RemoveStagedDescription(s.database, createRequest.Command.ID)
		}
		s.domainMu.Unlock()
		if err != nil {
			s.writeError(conn, request.ID, "command_error", err.Error())
			return
		}
		result = createResult
	case MethodManifestQueue:
		var queueRequest ManifestCommandQueueRequest
		if err := json.Unmarshal(request.Payload, &queueRequest); err != nil {
			s.writeError(conn, request.ID, "invalid_request", "decode manifest queue command: "+err.Error())
			return
		}
		if queueRequest.Command.Type != CommandStateSet || queueRequest.Command.TargetState != model.StateWaiting || queueRequest.Command.EventType != "work_item.state_set" || queueRequest.Command.Force {
			s.writeError(conn, request.ID, "invalid_request", "manifest queue command requires an ordinary state transition to waiting")
			return
		}
		s.domainMu.Lock()
		queueResult, err := s.database.ExecuteManifestCommandWithQueue(queueRequest.Command, queueRequest.Queue)
		if err == nil {
			err = MaterializeNativeWrites(context.Background(), s.database, s.native)
		}
		s.domainMu.Unlock()
		if err != nil {
			s.writeError(conn, request.ID, "command_error", err.Error())
			return
		}
		result = queueResult
	case MethodManifestCommand:
		var command ManifestCommand
		if err := json.Unmarshal(request.Payload, &command); err != nil {
			s.writeError(conn, request.ID, "invalid_request", "decode manifest command: "+err.Error())
			return
		}
		s.domainMu.Lock()
		commandResult, err := s.database.ExecuteManifestCommand(command)
		if err == nil {
			err = MaterializeNativeWrites(context.Background(), s.database, s.native)
		}
		s.domainMu.Unlock()
		if err != nil {
			s.writeError(conn, request.ID, "command_error", err.Error())
			return
		}
		result = commandResult
	case MethodListManifests:
		manifests, err := s.database.ListManifests()
		if err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		}
		if access == AccessAgent {
			for index := range manifests {
				manifests[index] = agentVisibleManifest(manifests[index])
			}
		}
		database, err := s.database.Status()
		if err != nil {
			s.writeError(conn, request.ID, "database_error", err.Error())
			return
		}
		projection := ProjectionMetadata{Revision: database.GlobalSequence, Source: "daemon-canonical", ObservedAt: time.Now().UTC(), Fresh: true, Warnings: []string{}}
		result = ManifestProjectionResult{Projection: projection, Manifests: manifests}
	default:
		s.writeError(conn, request.ID, "unknown_method", "unknown coordinator method "+request.Method)
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		s.writeError(conn, request.ID, "internal_error", err.Error())
		return
	}
	_ = json.NewEncoder(conn).Encode(Response{ProtocolVersion: ProtocolVersion, BuildIdentity: s.buildIdentity, ID: request.ID, OK: true, Result: encoded})
}

func agentMethodAllowed(method string) bool {
	switch method {
	case MethodPing, MethodStatus, MethodListManifests, MethodCreateItem:
		return true
	default:
		return false
	}
}

func agentVisibleManifest(manifest model.Manifest) model.Manifest {
	manifest.Repository = model.Repository{}
	manifest.Checkout = model.Checkout{Kind: manifest.Checkout.Kind}
	manifest.RootPiSession = nil
	return manifest
}

func validateAgentCreateItem(manifest model.Manifest) error {
	if manifest.State != model.StateBacklog {
		return errors.New("agent endpoint may create backlog items only")
	}
	if manifest.Checkout.Kind != model.WorkspaceKindManagedSlot || manifest.Checkout.Present() {
		return errors.New("agent endpoint may create absent managed-slot checkouts only")
	}
	if manifest.Checkout.Path != nil {
		return errors.New("agent endpoint may not claim a workspace path")
	}
	if manifest.Checkout.Branch != model.ItemBranchName(manifest.Slug, manifest.ID) {
		return errors.New("agent endpoint requires the canonical item branch")
	}
	if manifest.RootPiSession != nil {
		return errors.New("agent endpoint may not create conversation state")
	}
	return nil
}

func (s *Server) status() (Status, error) {
	database, err := s.database.Status()
	if err != nil {
		return Status{}, err
	}
	s.importMu.RLock()
	lastPiImport := s.lastPiImport
	s.importMu.RUnlock()
	return Status{ProtocolVersion: ProtocolVersion, BuildIdentity: s.buildIdentity, PID: os.Getpid(), StartedAt: s.startedAt.Format(time.RFC3339Nano), DataRoot: s.dataRoot, SocketPath: s.socketPath, AgentSocketPath: s.agentSocketPath, Access: AccessOperator, Database: database, PiImport: lastPiImport}, nil
}

func (s *Server) writeError(conn net.Conn, id, code, message string) {
	_ = json.NewEncoder(conn).Encode(Response{ProtocolVersion: ProtocolVersion, BuildIdentity: s.buildIdentity, ID: id, Error: &ProtocolError{Code: code, Message: message}})
}
