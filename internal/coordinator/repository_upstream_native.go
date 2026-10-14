package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// The acknowledgement is history of a Git edit, never current tracking policy.
func (d *Database) repositoryUpstreamOutcome(command SetRepositoryUpstreamCommand) (SetRepositoryUpstreamResult, bool, error) {
	var result SetRepositoryUpstreamResult
	found := false
	err := d.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketCommands).Get([]byte(command.ID)) != nil {
			return fmt.Errorf("command id %s was already used by another command", command.ID)
		}
		if encoded := tx.Bucket(bucketDomainCommands).Get([]byte(command.ID)); encoded != nil {
			var old repositoryUpstreamOutcome
			if err := json.Unmarshal(encoded, &old); err != nil {
				return err
			}
			a, _ := json.Marshal(old.Command)
			b, _ := json.Marshal(command)
			if !bytes.Equal(a, b) {
				return fmt.Errorf("command id %s was already used with different input", command.ID)
			}
			result = old.Result
			result.Duplicate = true
			found = true
			return nil
		}
		if pending := tx.Bucket(bucketProjections).Bucket([]byte(repositorySyncProjection)); pending != nil && pending.Get([]byte(command.RepositoryID)) != nil {
			return fmt.Errorf("finish or abort pending repository reconciliation before changing upstream")
		}
		return nil
	})
	return result, found, err
}
func (s *Server) setRepositoryUpstream(ctx context.Context, command SetRepositoryUpstreamCommand) (SetRepositoryUpstreamResult, error) {
	// URL/branch inputs name origin by default; native observations can include
	// a non-default remote or cache ref for compare-and-swap protection.
	for _, slot := range []**model.RepositoryUpstream{&command.Upstream, &command.PreviousUpstream} {
		if *slot != nil {
			copy := **slot
			if copy.Remote == "" {
				copy.Remote = "origin"
			}
			if copy.TrackingRef == "" {
				copy.TrackingRef = copy.Ref()
			}
			*slot = &copy
		}
	}
	if err := validateRepositoryUpstreamCommand(command); err != nil {
		return SetRepositoryUpstreamResult{}, err
	}
	source, guard, err := s.repositoryAccess(ctx, command.RepositoryID, false)
	if err != nil {
		return SetRepositoryUpstreamResult{}, err
	}
	defer guard.Release()
	if result, found, err := s.database.repositoryUpstreamOutcome(command); err != nil || found {
		return result, err
	}
	g := gitpkg.New("git")
	current, err := g.BranchUpstream(ctx, source.GitDir, source.Repository.IntegrationBranch)
	if err != nil {
		return SetRepositoryUpstreamResult{}, err
	}
	// A retained native step followed by a failed acknowledgement may be retried.
	// Accept only the requested configuration, never an unrelated concurrent edit.
	if !reflect.DeepEqual(current, command.Upstream) {
		if !reflect.DeepEqual(current, command.PreviousUpstream) {
			return SetRepositoryUpstreamResult{}, fmt.Errorf("Git upstream state changed; retry with its current configuration")
		}
		if err := g.SetBranchUpstream(ctx, source.GitDir, source.Repository.IntegrationBranch, current, command.Upstream); err != nil {
			return SetRepositoryUpstreamResult{}, err
		}
		current, err = g.BranchUpstream(ctx, source.GitDir, source.Repository.IntegrationBranch)
		if err != nil {
			return SetRepositoryUpstreamResult{}, fmt.Errorf("Git configuration updated; inspect it before retrying: %w", err)
		}
		if !reflect.DeepEqual(current, command.Upstream) {
			return SetRepositoryUpstreamResult{}, fmt.Errorf("Git configuration changed during upstream update; inspect it before retrying")
		}
	}
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	result, err := s.database.recordRepositoryUpstream(command, current)
	if err != nil {
		return result, fmt.Errorf("Git tracking configuration retained, but its acknowledgement failed: %w", err)
	}
	return result, nil
}
