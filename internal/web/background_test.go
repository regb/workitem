package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

type delayedBackend struct {
	fakeBackend
	finish          chan struct{}
	started         chan string
	active, maximum atomic.Int32
	count           int
}

func (b *delayedBackend) Create(_ context.Context, create Create) Outcome {
	b.count++
	id, _ := model.NewID()
	return Outcome{Observed: &model.Manifest{ID: id, Title: "New conversation", State: model.StateWorking}}
}
func (b *delayedBackend) StartCreated(ctx context.Context, id string) Outcome {
	n := b.active.Add(1)
	defer b.active.Add(-1)
	for previous := b.maximum.Load(); n > previous; previous = b.maximum.Load() {
		if b.maximum.CompareAndSwap(previous, n) {
			break
		}
	}
	b.started <- id
	select {
	case <-b.finish:
		return Outcome{Observed: &model.Manifest{ID: id, Title: "Generated title"}}
	case <-ctx.Done():
		return Outcome{Error: "Interrupted, conversation retained"}
	}
}
func TestReceiptDoesNotWaitForStartupOrUseRequestContext(t *testing.T) {
	b := &delayedBackend{finish: make(chan struct{}), started: make(chan string, 32)}
	s, _ := New(b, "127.0.0.1:1234")
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "http://"+s.host+"/api/items", strings.NewReader(`{"project_id":"project-a","prompt":"private task"}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("Origin", "http://"+s.host)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("receipt=%d %s", w.Code, w.Body.String())
	}
	var receipt Creation
	json.Unmarshal(w.Body.Bytes(), &receipt)
	if !model.ValidID(receipt.ID) || receipt.State != "queued" {
		t.Fatalf("receipt=%+v", receipt)
	}
	cancel()
	chatResponse := request(s, "GET", "/api/items/"+receipt.ID+"/chat", "")
	var loading Chat
	if chatResponse.Code != 200 || json.Unmarshal(chatResponse.Body.Bytes(), &loading) != nil || loading.Startup == nil || (loading.Startup.State != "queued" && loading.Startup.State != "starting") || loading.Item.ID != receipt.ID || len(loading.Warnings) > 0 {
		t.Fatalf("pending chat was inspected before startup: %s", chatResponse.Body.String())
	}
	select {
	case <-b.started:
	case <-time.After(time.Second):
		t.Fatal("startup not scheduled")
	}
	time.Sleep(10 * time.Millisecond)
	jobs := s.background.snapshot()
	if len(jobs) != 1 || jobs[0].State != "starting" {
		t.Fatalf("request cancellation cancelled startup: %+v", jobs)
	}
	close(b.finish)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jobs = s.background.snapshot()
		if jobs[0].State == "sent" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	jobs = s.background.snapshot()
	if jobs[0].State != "sent" || jobs[0].Title != "Generated title" {
		t.Fatalf("completion=%+v", jobs)
	}
	if strings.Contains(request(s, "GET", "/api/creations", "").Body.String(), "private task") {
		t.Fatal("receipt cache retained unrestricted prompt")
	}
}
func TestStartupQueueBoundsAndShutdown(t *testing.T) {
	b := &delayedBackend{finish: make(chan struct{}), started: make(chan string, 32)}
	s, _ := New(b, "127.0.0.1:1234")
	defer s.Close()
	for i := 0; i < 32; i++ {
		if w := request(s, "POST", "/api/items", `{"project_id":"project-a","prompt":"task"}`); w.Code != 202 {
			t.Fatalf("receipt %d: %s", i, w.Body.String())
		}
	}
	if w := request(s, "POST", "/api/items", `{"project_id":"project-a","prompt":"overflow"}`); w.Code != 429 || b.count != 32 {
		t.Fatalf("capacity created overflow item: %d count=%d", w.Code, b.count)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-b.started:
		case <-time.After(time.Second):
			t.Fatal("workers not started")
		}
	}
	if b.maximum.Load() > 4 {
		t.Fatalf("worker count=%d", b.maximum.Load())
	}
	s.Close()
	if len(s.background.slots) != 0 || b.active.Load() != 0 {
		t.Fatal("shutdown leaked pending workers")
	}
	for _, job := range s.background.snapshot() {
		if job.State != "problem" || job.Error == "" {
			t.Fatalf("interrupted item lost: %+v", job)
		}
	}
	if s.background.reserve() {
		t.Fatal("closed queue accepted work")
	}
}
