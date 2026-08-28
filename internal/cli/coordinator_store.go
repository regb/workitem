package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	storepkg "github.com/regb/workitem/internal/store"
)

func BindCoordinatorApp(application *app.App, native *storepkg.Store, client *coordinator.Client) {
	application.Store = newCoordinatorStore(native, client)
	application.DaemonSocketPath = client.SocketPath
	application.PiSessionObserver = coordinatorPiSessionObserver(client)
	application.WorkListObserver = coordinatorWorkListObserver(client)
}

// coordinatorStore makes the daemon the commit point for every mutation issued
// through App while keeping descriptions and Pi sessions available on disk.
type coordinatorStore struct {
	app.Store
	client *coordinator.Client
}

func newCoordinatorStore(base app.Store, client *coordinator.Client) app.Store {
	return &coordinatorStore{Store: base, client: client}
}

func (s *coordinatorStore) canonicalManifests() ([]model.Manifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.client.ListManifests(ctx)
}

func (s *coordinatorStore) ListManifests() ([]model.Manifest, []error) {
	manifests, err := s.canonicalManifests()
	if err != nil {
		return nil, []error{err}
	}
	return manifests, nil
}

func (s *coordinatorStore) LoadManifest(itemID string) (model.Manifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := s.client.CanonicalManifest(ctx, itemID)
	return result.Manifest, err
}

func (s *coordinatorStore) Resolve(selector string) (model.Manifest, error) {
	manifests, err := s.canonicalManifests()
	if err != nil {
		return model.Manifest{}, err
	}
	return storepkg.ResolveFromManifests(manifests, selector, func(manifest model.Manifest) string {
		content, _ := os.ReadFile(filepath.Join(s.ItemDir(manifest.ID), model.DescriptionFilename))
		return string(content)
	})
}

func (s *coordinatorStore) ResolveActiveSlug(selector string) (model.Manifest, error) {
	manifests, err := s.canonicalManifests()
	if err != nil {
		return model.Manifest{}, err
	}
	return storepkg.ResolveActiveSlugFromManifests(manifests, selector)
}

func (s *coordinatorStore) FindByWorktree(checkoutPath string) (model.Manifest, error) {
	manifests, err := s.canonicalManifests()
	if err != nil {
		return model.Manifest{}, err
	}
	return storepkg.FindByWorktreeFromManifests(manifests, checkoutPath)
}

func (s *coordinatorStore) resources(itemID string) (coordinator.ItemResourcesResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.client.ItemResources(ctx, itemID)
}

func (s *coordinatorStore) LoadAgentRuntime(itemID string) (*model.AgentRuntime, error) {
	resources, err := s.resources(itemID)
	if err != nil || resources.Runtime == nil {
		return resources.Runtime, err
	}
	runtime := *resources.Runtime
	return &runtime, nil
}

func (s *coordinatorStore) LoadTerminalRuntime(itemID string) (*model.TerminalRuntime, error) {
	resources, err := s.resources(itemID)
	return resources.Terminal, err
}

func (s *coordinatorStore) execute(ctx context.Context, operation, itemID string, value any) error {
	commandID, err := model.NewID()
	if err != nil {
		return err
	}
	switch typed := value.(type) {
	case model.Event:
		if typed.Data == nil {
			typed.Data = map[string]any{}
		}
		typed.Data["command_id"] = commandID
		value = typed
	}
	payload := json.RawMessage("null")
	if value != nil {
		payload, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	createdAt := time.Now().UTC()
	for attempt := 0; attempt < 16; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		canonical, err := s.client.CanonicalManifest(requestCtx, itemID)
		cancel()
		if err != nil {
			return err
		}
		command := coordinator.StoreCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Operation: operation, ItemID: itemID, ExpectedRevision: &canonical.Revision, PayloadDigest: coordinator.StorePayloadDigest(payload), CreatedAt: createdAt}
		requestCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
		_, err = s.client.ExecuteStoreCommand(requestCtx, coordinator.StoreCommandRequest{Command: command, Payload: payload})
		cancel()
		if err == nil {
			return nil
		}
		if !stringsContainsRevisionConflict(err.Error()) {
			return err
		}
	}
	return fmt.Errorf("store command revision retries exhausted for %s", itemID)
}

func (s *coordinatorStore) CreateItem(ctx context.Context, manifest model.Manifest, events ...model.Event) error {
	commandID, err := model.NewID()
	if err != nil {
		return err
	}
	request := coordinator.CreateItemRequest{Command: coordinator.CreateItemCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Manifest: manifest, DescriptionDigest: coordinator.DescriptionDigest(""), Actor: "user", CreatedAt: manifest.CreatedAt}}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = s.client.CreateItem(requestCtx, request)
	cancel()
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.Type == "work_item.created" {
			continue
		}
		if err := s.AppendEvent(ctx, manifest.ID, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *coordinatorStore) SaveManifest(ctx context.Context, manifest model.Manifest) error {
	return s.execute(ctx, coordinator.StoreManifestSave, manifest.ID, manifest)
}
func (s *coordinatorStore) ClaimRepositoryHome(ctx context.Context, manifest model.Manifest) error {
	return s.SaveManifest(ctx, manifest)
}
func (s *coordinatorStore) AppendEvent(ctx context.Context, id string, event model.Event) error {
	return s.execute(ctx, coordinator.StoreEventAppend, id, event)
}
func (s *coordinatorStore) ReadEvents(id string) ([]model.Event, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.client.ItemEvents(ctx, id)
}
func (s *coordinatorStore) SaveTerminalRuntime(ctx context.Context, id string, runtime model.TerminalRuntime) error {
	return s.execute(ctx, coordinator.StoreTerminalRuntimeSave, id, runtime)
}
func (s *coordinatorStore) RemoveTerminalRuntime(ctx context.Context, id string) error {
	return s.execute(ctx, coordinator.StoreTerminalRuntimeRemove, id, nil)
}
func (s *coordinatorStore) SaveAgentRuntime(ctx context.Context, id string, runtime model.AgentRuntime) error {
	return s.execute(ctx, coordinator.StoreAgentRuntimeSave, id, runtime)
}
func (s *coordinatorStore) RemoveItem(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.execute(ctx, coordinator.StoreItemDelete, id, nil)
}
