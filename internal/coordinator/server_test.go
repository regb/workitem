package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestServerStatusAndShutdown(t *testing.T) {
	root, runtime := t.TempDir(), t.TempDir()
	socket, err := SocketPath(runtime, root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	client := &Client{SocketPath: socket}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	skewed := &Client{SocketPath: socket, BuildIdentity: "different-build"}
	var compatibility *CompatibilityError
	if err := skewed.Ping(ctx); !errors.As(err, &compatibility) || compatibility.Kind != "build" {
		t.Fatalf("skewed ping error = %v", err)
	}
	status, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PID != os.Getpid() || status.BuildIdentity == "" || status.DataRoot != root || status.Database.SchemaVersion != SchemaVersion {
		t.Fatalf("status = %+v", status)
	}
	projection, err := client.ManifestProjection(ctx)
	if err != nil || len(projection.Manifests) != 0 || !projection.Projection.Fresh || projection.Projection.Source != "daemon-canonical" {
		t.Fatalf("projection = %+v, err = %v", projection, err)
	}
	resources, err := client.ItemResources(ctx, importTestID)
	if err != nil || resources.Runtime != nil || resources.Terminal != nil {
		t.Fatalf("item resources = %+v, err = %v", resources, err)
	}
	piProjection, err := client.PiSession(ctx, importTestID)
	if err != nil || piProjection.Found {
		t.Fatalf("Pi projection = %+v, err = %v", piProjection, err)
	}
	barrier, err := client.ActivityBarrier(ctx)
	if err != nil || !barrier.Projection.Fresh || barrier.Projection.Source != "daemon.activity_barrier" {
		t.Fatalf("activity barrier = %+v, err = %v", barrier, err)
	}
	snapshot, err := client.ActionabilitySnapshot(ctx, ActionabilitySnapshotRequest{Queue: ActionabilityQueueOptions{}})
	if err != nil || !snapshot.Projection.Fresh || len(snapshot.Queue.Candidates) != 0 {
		t.Fatalf("actionability snapshot = %+v, err = %v", snapshot, err)
	}
	revision := uint64(0)
	_, err = client.ExecuteManifestCommandWithQueue(ctx, ManifestCommandQueueRequest{Command: ManifestCommand{ID: "invalid-queue", ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: importTestID, ExpectedRevision: &revision, Actor: "user", TargetState: model.StateWorking, CreatedAt: time.Now().UTC()}})
	if err == nil || !strings.Contains(err.Error(), "transition to waiting") {
		t.Fatalf("invalid manifest queue command err=%v", err)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket info=%v err=%v", info, err)
	}
	if err := client.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server did not stop")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket remains after shutdown: %v", err)
	}
}

func TestAgentSocketCreatesOnlySafeBacklogItems(t *testing.T) {
	root, runtime := t.TempDir(), t.TempDir()
	operatorSocket, err := SocketPath(runtime, root)
	if err != nil {
		t.Fatal(err)
	}
	agentSocket, err := AgentSocketPath(runtime, root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServerWithAgentSocket(root, operatorSocket, agentSocket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	operator, agentClient := &Client{SocketPath: operatorSocket}, &Client{SocketPath: agentSocket}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := agentClient.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Access != AccessAgent || status.SocketPath != agentSocket || status.AgentSocketPath != "" || status.DataRoot != "" || status.Database.Path != "" {
		t.Fatalf("agent status = %+v", status)
	}
	commit := strings.Repeat("a", 40)
	manifest := model.NewManifest(importTestID, "agent-filed", "Agent filed", nil, false, time.Now().UTC(), model.Repository{RootAtCreation: "/repo", GitCommonDir: "/repo/.git", CreatedFromCommit: commit}, model.Checkout{Kind: model.WorkspaceKindManagedSlot, Branch: model.ItemBranchName("agent-filed", importTestID)})
	command := CreateItemCommand{ID: "agent-create", ProtocolVersion: ProtocolVersion, Manifest: manifest, DescriptionDigest: DescriptionDigest("follow-up"), Actor: "user", CreatedAt: manifest.CreatedAt}
	created, err := agentClient.CreateItem(ctx, CreateItemRequest{Command: command, Description: "follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest.State != model.StateBacklog || created.Event.Actor != "agent" {
		t.Fatalf("created = %+v", created)
	}
	agentProjection, err := agentClient.ManifestProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agentProjection.Manifests) != 1 || agentProjection.Manifests[0].Repository.RootAtCreation != "" || agentProjection.Manifests[0].Repository.GitCommonDir != "" || agentProjection.Manifests[0].Checkout.Branch != "" || agentProjection.Manifests[0].RootPiSession != nil {
		t.Fatalf("agent projection exposed source, checkout, or conversation details: %+v", agentProjection.Manifests)
	}
	operatorProjection, err := operator.ManifestProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(operatorProjection.Manifests) != 1 || operatorProjection.Manifests[0].Repository.RootAtCreation != "/repo" || operatorProjection.Manifests[0].Checkout.Branch == "" {
		t.Fatalf("operator projection was unexpectedly sanitized: %+v", operatorProjection.Manifests)
	}
	if err := agentClient.Shutdown(ctx); err == nil || !strings.Contains(err.Error(), "does not allow shutdown") {
		t.Fatalf("agent shutdown error = %v", err)
	}
	homeID := "01K00000000000000000000001"
	home := model.NewManifest(homeID, "unsafe-home", "Unsafe home", nil, false, time.Now().UTC(), manifest.Repository, model.Checkout{Kind: model.WorkspaceKindRepositoryHome, Branch: "main"})
	homePath := "/repo"
	home.Checkout.Path = &homePath
	homeCommand := CreateItemCommand{ID: "agent-home", ProtocolVersion: ProtocolVersion, Manifest: home, DescriptionDigest: DescriptionDigest("unsafe"), Actor: "user", CreatedAt: home.CreatedAt}
	if _, err := agentClient.CreateItem(ctx, CreateItemRequest{Command: homeCommand, Description: "unsafe"}); err == nil || !strings.Contains(err.Error(), "managed-slot") {
		t.Fatalf("agent home creation error = %v", err)
	}
	if err := operator.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server did not stop")
	}
	for _, path := range []string{operatorSocket, agentSocket} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("socket remains after shutdown: %s: %v", path, err)
		}
	}
}

func TestActivityBarrierIndexesPromptBeforeReturningSnapshot(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	manifest := model.NewManifest(importTestID, "barrier", "Barrier", nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateWorking
	manifest.RootPiSession = &model.PiSession{ID: "session-1", Path: "sessions/pi/session-1.jsonl"}
	if err := native.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(native.ItemDir(manifest.ID), filepath.FromSlash(manifest.RootPiSession.Path))
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	appendMessage := func(at time.Time, role, text string) {
		f, err := os.OpenFile(sessionPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		value := map[string]any{"type": "message", "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"role": role, "content": []map[string]any{{"type": "text", "text": text}}}}
		if err := json.NewEncoder(f).Encode(value); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage(now, "assistant", "initial")
	if err := seedRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	socket, _ := SocketPath(t.TempDir(), root)
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	promptAt := now.Add(time.Second)
	appendMessage(promptAt, "user", "PRIVATE_BARRIER_PROMPT")
	result, err := server.activityBarrier(context.Background())
	if err != nil || !result.Projection.Fresh || result.PiImport.EntriesScanned != 1 || result.PiImport.BytesRead == 0 {
		t.Fatalf("barrier=%+v err=%v", result, err)
	}
	var observation AgentObservation
	found, err := server.database.ReadProjection(AgentObservationProjection, manifest.ID, &observation)
	if err != nil || !found || observation.Activity.LastRequestedAt == nil || !observation.Activity.LastRequestedAt.Equal(promptAt) {
		t.Fatalf("observation=%+v found=%v err=%v", observation, found, err)
	}
	databaseBytes, err := os.ReadFile(DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(databaseBytes), "PRIVATE_BARRIER_PROMPT") {
		t.Fatal("barrier copied prompt content into wi.db")
	}
}

func TestServerRejectsProtocolMismatch(t *testing.T) {
	root, runtime := t.TempDir(), t.TempDir()
	socket, _ := SocketPath(runtime, root)
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go server.Serve(context.Background())
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"protocol_version":999,"id":"bad","method":"ping"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buffer[:n]), "protocol_mismatch") {
		t.Fatalf("response = %s", buffer[:n])
	}
}

func TestServerRefusesToReplaceNonSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wi", "blocked.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(t.TempDir(), path); err == nil {
		t.Fatal("expected non-socket refusal")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "do not replace" {
		t.Fatalf("non-socket changed: %q err=%v", content, err)
	}
}
