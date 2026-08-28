package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/version"
)

type Client struct {
	SocketPath    string
	BuildIdentity string
	counter       atomic.Uint64
}

func (c *Client) Call(ctx context.Context, method string, payload any, result any) error {
	if c == nil || c.SocketPath == "" {
		return errors.New("coordinator socket path is required")
	}
	var encoded json.RawMessage
	if payload != nil {
		value, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		encoded = value
	}
	if len(encoded) > MaxRequestBytes {
		return fmt.Errorf("coordinator request payload exceeds %d bytes", MaxRequestBytes)
	}
	requestID := fmt.Sprintf("client-%d-%d", time.Now().UnixNano(), c.counter.Add(1))
	buildIdentity := c.BuildIdentity
	if buildIdentity == "" {
		buildIdentity = version.BuildIdentity()
	}
	request := Request{ProtocolVersion: ProtocolVersion, BuildIdentity: buildIdentity, ID: requestID, Method: method, Payload: encoded}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return fmt.Errorf("connect to wi daemon: %w", err)
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("coordinator connection is not a Unix socket")
	}
	if err := validatePeerOwnership(unixConn); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return fmt.Errorf("send coordinator request: %w", err)
	}
	var response Response
	if err := json.NewDecoder(io.LimitReader(conn, MaxResponseBytes+1)).Decode(&response); err != nil {
		return fmt.Errorf("read coordinator response: %w", err)
	}
	if response.ProtocolVersion != ProtocolVersion {
		return &CompatibilityError{Kind: "protocol", Expected: fmt.Sprint(ProtocolVersion), Actual: fmt.Sprint(response.ProtocolVersion)}
	}
	if response.ID != requestID {
		return fmt.Errorf("invalid coordinator response identity")
	}
	if method != MethodShutdown && method != MethodRuntimeEvent && response.BuildIdentity != buildIdentity {
		return &CompatibilityError{Kind: "build", Expected: buildIdentity, Actual: response.BuildIdentity}
	}
	if !response.OK {
		if response.Error == nil {
			return errors.New("coordinator rejected request")
		}
		return response.Error
	}
	if result != nil && len(response.Result) > 0 {
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode coordinator result: %w", err)
		}
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) error {
	var response struct {
		Pong bool `json:"pong"`
	}
	if err := c.Call(ctx, MethodPing, nil, &response); err != nil {
		return err
	}
	if !response.Pong {
		return errors.New("coordinator did not acknowledge ping")
	}
	return nil
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.Call(ctx, MethodStatus, nil, &status)
	return status, err
}

func (c *Client) Shutdown(ctx context.Context) error {
	return c.Call(ctx, MethodShutdown, nil, nil)
}

// ShutdownProtocol is used only to replace a daemon speaking the immediately
// previous protocol. Shutdown remains intentionally payload-free.
func (c *Client) ShutdownProtocol(ctx context.Context, protocolVersion int) error {
	requestID := fmt.Sprintf("compat-shutdown-%d", time.Now().UnixNano())
	request := Request{ProtocolVersion: protocolVersion, ID: requestID, Method: MethodShutdown}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("coordinator connection is not a Unix socket")
	}
	if err := validatePeerOwnership(unixConn); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	var response Response
	if err := json.NewDecoder(io.LimitReader(conn, MaxResponseBytes+1)).Decode(&response); err != nil {
		return err
	}
	if response.ID != requestID || !response.OK {
		return errors.New("older daemon refused graceful shutdown")
	}
	return nil
}

func (c *Client) ManifestProjection(ctx context.Context) (ManifestProjectionResult, error) {
	var result ManifestProjectionResult
	err := c.Call(ctx, MethodListManifests, nil, &result)
	return result, err
}

func (c *Client) PiSession(ctx context.Context, itemID string) (PiSessionProjectionResult, error) {
	var result PiSessionProjectionResult
	err := c.Call(ctx, MethodPiSession, ItemRequest{ItemID: itemID}, &result)
	return result, err
}

func (c *Client) AgentObservation(ctx context.Context, itemID string) (AgentObservationResult, error) {
	var result AgentObservationResult
	err := c.Call(ctx, MethodAgentObservation, ItemRequest{ItemID: itemID}, &result)
	return result, err
}

func (c *Client) ItemResources(ctx context.Context, itemID string) (ItemResourcesResult, error) {
	var result ItemResourcesResult
	err := c.Call(ctx, MethodItemResources, ItemRequest{ItemID: itemID}, &result)
	return result, err
}

func (c *Client) CanonicalManifest(ctx context.Context, itemID string) (CanonicalManifest, error) {
	var result CanonicalManifest
	err := c.Call(ctx, MethodCanonicalManifest, ItemRequest{ItemID: itemID}, &result)
	return result, err
}

func (c *Client) RuntimeEvents(ctx context.Context, request RuntimeEventsRequest) (RuntimeEventsResult, error) {
	var result RuntimeEventsResult
	err := c.Call(ctx, MethodRuntimeEvents, request, &result)
	return result, err
}

func (c *Client) ItemEvents(ctx context.Context, itemID string) ([]model.Event, error) {
	var result ItemEventsResult
	err := c.Call(ctx, MethodItemEvents, ItemRequest{ItemID: itemID}, &result)
	return result.Events, err
}

func (c *Client) ActivityBarrier(ctx context.Context) (ActivityBarrierResult, error) {
	var result ActivityBarrierResult
	err := c.Call(ctx, MethodActivityBarrier, nil, &result)
	return result, err
}

func (c *Client) ActionabilitySnapshot(ctx context.Context, request ActionabilitySnapshotRequest) (ActionabilitySnapshotResult, error) {
	var result ActionabilitySnapshotResult
	err := c.Call(ctx, MethodActionability, request, &result)
	return result, err
}

func (c *Client) IngestRuntimeEvent(ctx context.Context, event RuntimeSemanticEvent) (RuntimeIngestResult, error) {
	var result RuntimeIngestResult
	err := c.Call(ctx, MethodRuntimeEvent, event, &result)
	return result, err
}

func (c *Client) ExecuteStoreCommand(ctx context.Context, request StoreCommandRequest) (StoreCommandResult, error) {
	var result StoreCommandResult
	err := c.Call(ctx, MethodStoreCommand, request, &result)
	return result, err
}

func (c *Client) CreateItem(ctx context.Context, request CreateItemRequest) (CreateItemResult, error) {
	var result CreateItemResult
	err := c.Call(ctx, MethodCreateItem, request, &result)
	return result, err
}

func (c *Client) ExecuteManifestCommand(ctx context.Context, command ManifestCommand) (ManifestCommandResult, error) {
	var result ManifestCommandResult
	err := c.Call(ctx, MethodManifestCommand, command, &result)
	return result, err
}

func (c *Client) ExecuteManifestCommandWithQueue(ctx context.Context, request ManifestCommandQueueRequest) (ManifestCommandQueueResult, error) {
	var result ManifestCommandQueueResult
	err := c.Call(ctx, MethodManifestQueue, request, &result)
	return result, err
}

func (c *Client) ListManifests(ctx context.Context) ([]model.Manifest, error) {
	result, err := c.ManifestProjection(ctx)
	return result.Manifests, err
}
