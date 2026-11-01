package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	itemlock "github.com/regb/workitem/internal/lock"
)

// Setup statuses. A run still "running" when its lock is free was
// interrupted.
const (
	SetupRunning   = "running"
	SetupSucceeded = "succeeded"
	SetupFailed    = "failed"
	SetupSkipped   = "skipped"
)

// SetupRun records the setup of one checkout claim: an execution fact kept
// under XDG state, never item lifecycle. Its output is only in Log.
type SetupRun struct {
	ClaimID    string     `json:"claim_id"`
	Generation uint64     `json:"generation"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	Warnings   []string   `json:"warnings,omitempty"`
	Log        string     `json:"log,omitempty"`
}

// Ready reports whether an agent may start on this run's claim.
func (r SetupRun) Ready(claimID string) bool {
	return r.ClaimID == claimID && (r.Status == SetupSucceeded || r.Status == SetupSkipped)
}

// SetupRuns keeps each item's latest setup run, its log and its lock in a
// state directory.
type SetupRuns struct{ Dir string }

func (s SetupRuns) Path(id, suffix string) string { return filepath.Join(s.Dir, id+suffix) }

// Read returns the item's latest run, zero when it has none. A run left
// running by a process that is gone reads as failed.
func (s SetupRuns) Read(id string) (SetupRun, error) {
	var run SetupRun
	if s.Dir == "" {
		return run, nil
	}
	content, err := os.ReadFile(s.Path(id, ".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return run, nil
	}
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal(content, &run); err != nil {
		return run, err
	}
	if run.Status == SetupRunning {
		// A run is only running while its lock is held.
		if lock, err := itemlock.TryAcquire(s.Path(id, ".lock")); err == nil {
			_ = lock.Release()
			run.Status, run.Error = SetupFailed, "setup was interrupted"
		}
	}
	return run, nil
}

func (s SetupRuns) Write(id string, run SetupRun) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(run)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.Path(id, ".json"), encoded)
}

func writeFileAtomic(path string, content []byte) error {
	staged, err := os.CreateTemp(filepath.Dir(path), ".setup-*")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	if _, err := staged.Write(content); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return os.Rename(staged.Name(), path)
}
