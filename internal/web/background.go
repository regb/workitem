package web

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/regb/workitem/internal/model"
)

// Creation is a rebuildable startup receipt, never a conversation or runtime owner.
type Creation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	Warnings  []string  `json:"warnings,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
type backgroundStarts struct {
	backend        Backend
	ctx            context.Context
	cancel         context.CancelFunc
	slots, workers chan struct{}
	mu             sync.Mutex
	jobs           map[string]Creation
	group          sync.WaitGroup
	closed         bool
}

func newBackgroundStarts(backend Backend) *backgroundStarts {
	ctx, cancel := context.WithCancel(context.Background())
	return &backgroundStarts{backend: backend, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 32), workers: make(chan struct{}, 4), jobs: map[string]Creation{}}
}
func (b *backgroundStarts) reserve() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	select {
	case b.slots <- struct{}{}:
		return true
	default:
		return false
	}
}
func (b *backgroundStarts) release() { <-b.slots }
func (b *backgroundStarts) enqueue(m model.Manifest, warnings []string) Creation {
	job := Creation{ID: m.ID, Title: m.Title, State: "queued", Warnings: append([]string(nil), warnings...), CreatedAt: time.Now().UTC()}
	b.mu.Lock()
	b.prune()
	if b.closed {
		job.State = "problem"
		job.Error = "Startup interrupted; the saved conversation can be started with the CLI."
		b.jobs[job.ID] = job
		b.mu.Unlock()
		b.release()
		return job
	}
	b.jobs[job.ID] = job
	b.group.Add(1)
	b.mu.Unlock()
	go b.run(job.ID)
	return job
}

// Caller holds mu. Pending jobs are never evicted.
func (b *backgroundStarts) prune() {
	if len(b.jobs) < 128 {
		return
	}
	var oldest Creation
	for _, previous := range b.jobs {
		if previous.State == "queued" || previous.State == "starting" {
			continue
		}
		if oldest.ID == "" || previous.CreatedAt.Before(oldest.CreatedAt) {
			oldest = previous
		}
	}
	if oldest.ID != "" {
		delete(b.jobs, oldest.ID)
	}
}
func (b *backgroundStarts) recordProblem(m model.Manifest, message string, warnings []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune()
	b.jobs[m.ID] = Creation{ID: m.ID, Title: m.Title, State: "problem", Error: message, Warnings: append([]string(nil), warnings...), CreatedAt: time.Now().UTC()}
}
func (b *backgroundStarts) run(id string) {
	defer b.group.Done()
	defer b.release()
	out := Outcome{}
	defer func() {
		if recover() != nil {
			out.Error = "Background startup failed; the conversation is retained."
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		job := b.jobs[id]
		job.State = "sent"
		if out.Error != "" {
			job.State = "problem"
			job.Error = out.Error
		}
		job.Warnings = append(job.Warnings, out.Warnings...)
		if out.Observed != nil {
			job.Title = out.Observed.Title
		}
		b.jobs[id] = job
	}()
	select {
	case b.workers <- struct{}{}:
		defer func() { <-b.workers }()
	case <-b.ctx.Done():
		out.Error = "Startup interrupted; the saved conversation can be started with the CLI."
		return
	}
	b.mu.Lock()
	job := b.jobs[id]
	job.State = "starting"
	b.jobs[id] = job
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(b.ctx, 2*time.Minute)
	defer cancel()
	out = b.backend.StartCreated(ctx, id)
}
func (b *backgroundStarts) lookup(id string) (Creation, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	job, ok := b.jobs[id]
	job.Warnings = append([]string(nil), job.Warnings...)
	return job, ok
}
func (b *backgroundStarts) snapshot() []Creation {
	b.mu.Lock()
	defer b.mu.Unlock()
	jobs := make([]Creation, 0, len(b.jobs))
	for _, job := range b.jobs {
		job.Warnings = append([]string(nil), job.Warnings...)
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	return jobs
}
func (b *backgroundStarts) close() {
	b.mu.Lock()
	b.closed = true
	b.cancel()
	b.mu.Unlock()
	b.group.Wait()
}
