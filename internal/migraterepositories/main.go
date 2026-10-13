// One-shot, operator-reviewed offline conversion. Not part of the wi CLI.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	bolt "go.etcd.io/bbolt"
)

// Historical migration plans retain their source remote independently of the
// current registry schema. It is never written into a new repository record.
type migrationRepositoryRecord struct {
	model.OwnedRepository
	OriginURL string `json:"origin_url,omitempty"`
}
type RepositoryPlan struct {
	Record migrationRepositoryRecord
	Source string
	Common string
	Pins   []string
}
type ItemPlan struct {
	DirectoryDigests map[string]string
	Before           model.Manifest
	After            model.Manifest
	Snapshot         *gitpkg.DirtySnapshot
	Source           string
}
type Plan struct {
	Root         string
	CreatedAt    time.Time
	Repositories []RepositoryPlan
	Items        []ItemPlan
	Warnings     []string
}

var git = gitpkg.New("git")
var ctx = context.Background()

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func uintBytes(x uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, x); return b }
func number(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
func save(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	must(err)
	must(os.WriteFile(path, b, 0600))
}
func main() {
	root := flag.String("root", "", "real data root")
	input := flag.String("input", "", "daemon-exported manifest array")
	planPath := flag.String("plan", "", "protected plan file")
	mode := flag.String("mode", "prepare", "prepare|verify|convert")
	database := flag.String("database", "", "staging database path")
	flag.Parse()
	if *root == "" || *planPath == "" {
		panic("root and plan are required")
	}
	var p Plan
	if *mode == "prepare" {
		if _, err := os.Stat(*planPath); err == nil {
			panic("plan already exists; verify or review it instead of replacing it")
		}
		data, err := os.ReadFile(*input)
		must(err)
		var manifests []model.Manifest
		must(json.Unmarshal(data, &manifests))
		p = prepare(*root, manifests)
		save(*planPath, p)
	} else {
		data, err := os.ReadFile(*planPath)
		must(err)
		must(json.Unmarshal(data, &p))
		if p.Root != *root {
			panic("data root differs from plan")
		}
	}
	verify(p)
	if *mode == "convert" {
		if *database == "" {
			panic("staging database is required")
		}
		convert(*database, p)
	}
	fmt.Printf("Verified %d repositories, %d items; %d warnings\n", len(p.Repositories), len(p.Items), len(p.Warnings))
	for _, w := range p.Warnings {
		fmt.Println("WARNING:", w)
	}
}

func prepare(root string, ms []model.Manifest) Plan {
	p := Plan{Root: root, CreatedAt: time.Now().UTC()}
	groups := map[string]int{}
	sources := map[string]int{}
	usedNames := map[string]bool{}
	aliases := map[int]map[string]bool{}
	// Discover intact common directories first. Missing archived roots may use a
	// unique same-origin candidate only after proving its created-from commit.
	for _, m := range ms {
		candidates := []string{m.Repository.OperationalRoot()}
		if m.Checkout.Claim != nil {
			candidates = append(candidates, m.Checkout.Claim.Path)
		}
		for _, path := range candidates {
			info, err := git.DetectRepository(ctx, path, m.Repository.CreatedFromCommit)
			if err != nil {
				continue
			}
			common := info.Repository.GitCommonDir
			n, ok := groups[common]
			if !ok {
				id, err := model.NewID()
				must(err)
				branch, err := git.DefaultBranch(ctx, path)
				must(err)
				name := repositoryName(info.Repository)
				if usedNames[name] {
					name += "-" + strings.ToLower(id[len(id)-6:])
				}
				usedNames[name] = true
				record, err := model.NormalizeOwnedRepository(model.OwnedRepository{ID: id, Name: name, IntegrationBranch: branch, CreatedAt: p.CreatedAt})
				must(err)
				n = len(p.Repositories)
				groups[common] = n
				p.Repositories = append(p.Repositories, RepositoryPlan{Record: migrationRepositoryRecord{OwnedRepository: record, OriginURL: info.Repository.RemoteURL}, Source: path, Common: common})
			}
			if aliases[n] == nil {
				aliases[n] = map[string]bool{}
			}
			aliases[n][m.Repository.RemoteURL] = true
			sources[m.ID] = n
			break
		}
	}
	for _, m := range ms {
		if _, ok := sources[m.ID]; ok {
			continue
		}
		candidate := -1
		for n, r := range p.Repositories {
			if m.Repository.RemoteURL == "" || (!aliases[n][m.Repository.RemoteURL] && r.Record.OriginURL != m.Repository.RemoteURL) {
				continue
			}
			if sha, err := git.RevParse(ctx, r.Source, m.Repository.CreatedFromCommit+"^{commit}"); err == nil && sha == m.Repository.CreatedFromCommit {
				if candidate >= 0 {
					panic("ambiguous replacement source for " + m.ID)
				}
				candidate = n
			}
		}
		if candidate < 0 {
			panic("no recoverable source for " + m.ID)
		}
		sources[m.ID] = candidate
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s: stale source %s; verified replacement %s", m.ID, m.Repository.OperationalRoot(), p.Repositories[candidate].Source))
	}
	for _, m := range ms {
		n := sources[m.ID]
		r := &p.Repositories[n]
		r.Pins = append(r.Pins, m.Repository.CreatedFromCommit)
		after := m
		after.Repository = model.Repository{ID: r.Record.ID, CreatedFromCommit: m.Repository.CreatedFromCommit}
		branch := m.Checkout.Specification.Branch
		if m.Checkout.Specification.Kind == model.LegacyWorkspaceKindRepositoryHome {
			branch = model.ItemBranchName(m.Slug, m.ID)
		}
		after.Checkout.Specification = model.CheckoutSpecification{Kind: model.WorkspaceKindManagedSlot, Executor: model.ExecutorLocal, Branch: branch}
		item := ItemPlan{Before: m, After: after}
		if claim := m.Checkout.Claim; claim != nil {
			snapshot, err := git.CaptureDirtySnapshot(ctx, claim.Path)
			must(err)
			current, err := git.CurrentBranch(ctx, claim.Path)
			must(err)
			if m.Checkout.Specification.Kind == model.WorkspaceKindManagedSlot && current != m.Checkout.Specification.Branch {
				panic("managed divergence requires explicit plan: " + m.ID)
			}
			item.DirectoryDigests = map[string]string{}
			for _, name := range snapshot.Directories {
				digest, err := directoryDigest(filepath.Join(claim.Path, name))
				must(err)
				item.DirectoryDigests[name] = digest
			}
			item.Snapshot = &snapshot
			item.Source = claim.Path
			r.Pins = append(r.Pins, snapshot.Head)
		}
		p.Items = append(p.Items, item)
	}
	for _, r := range p.Repositories {
		path, err := model.OwnedRepositoryGitDir(root, r.Record.ID)
		must(err)
		fmt.Println("Import", r.Record.Name, r.Source, "->", path)
		must(git.ImportBare(ctx, model.BareImportOptions{Source: r.Source, Destination: path, OriginURL: r.Record.OriginURL, IntegrationBranch: r.Record.IntegrationBranch, RequiredCommits: r.Pins}))
	}
	slots := map[string]int{}
	for i := range p.Items {
		item := &p.Items[i]
		r := p.Repositories[sources[item.Before.ID]]
		bare, err := model.OwnedRepositoryGitDir(root, r.Record.ID)
		must(err)
		if item.Snapshot == nil {
			if exists, err := git.BranchExists(ctx, bare, item.After.Checkout.Specification.Branch); err != nil {
				panic(err)
			} else if !exists && item.Before.Checkout.Specification.Kind == model.WorkspaceKindManagedSlot {
				p.Warnings = append(p.Warnings, "historical implementation branch is absent: "+item.Before.ID+" "+item.After.Checkout.Specification.Branch)
			}
			continue
		}
		slots[r.Record.ID]++
		slot := fmt.Sprintf("slot-%04d", slots[r.Record.ID])
		path := filepath.Join(root, "worktrees", r.Record.ID, slot)
		branch := item.After.Checkout.Specification.Branch
		exists, err := git.BranchExists(ctx, bare, branch)
		must(err)
		if exists {
			sha, err := git.ResolveBranchSHA(ctx, bare, branch)
			must(err)
			if sha != item.Snapshot.Head {
				panic("branch tip differs from captured HEAD: " + item.Before.ID)
			}
		}
		fmt.Println("Restore", item.Before.ID, path)
		must(git.WorktreeAdd(ctx, model.WorktreeAddOptions{RepoRoot: bare, Path: path, Branch: branch, NewBranch: !exists, StartPoint: item.Snapshot.Head}))
		regular := *item.Snapshot
		regular.Directories = nil
		must(git.RestoreDirtySnapshot(ctx, path, regular))
		for name, digest := range item.DirectoryDigests {
			must(copyDirectory(filepath.Join(item.Source, name), filepath.Join(path, name), digest))
		}
		item.After.Checkout.ClearClaim()
		_, err = item.After.Checkout.AssignNewClaim(model.ExecutorLocal, r.Record.ID, slot, path)
		must(err)
		normalized, err := store.NormalizeManifest(item.After)
		must(err)
		item.After = normalized
	}
	return p
}
func repositoryName(r model.Repository) string {
	value := strings.TrimSuffix(strings.TrimRight(r.RemoteURL, "/"), ".git")
	if value != "" {
		parts := strings.FieldsFunc(value, func(c rune) bool { return c == '/' || c == ':' })
		value = parts[len(parts)-1]
		if value == "monorepo" && len(parts) > 1 {
			value = parts[len(parts)-2] + "-monorepo"
		}
	} else {
		value = filepath.Base(filepath.Dir(r.GitCommonDir))
		if filepath.Base(r.GitCommonDir) != ".git" {
			value = filepath.Base(r.GitCommonDir)
		}
	}
	value = strings.ToLower(value)
	value = strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			return c
		}
		return '-'
	}, value)
	value = strings.Trim(value, "._-")
	if len(value) > 48 {
		value = value[:48]
	}
	if value == "" {
		value = "repository"
	}
	return value
}
func verify(p Plan) {
	paths := map[string]bool{}
	claims := map[string]bool{}
	for _, r := range p.Repositories {
		bare, err := model.OwnedRepositoryGitDir(p.Root, r.Record.ID)
		must(err)
		must(git.ValidateOwnedBare(ctx, bare, r.Record.IntegrationBranch))
		for _, sha := range r.Pins {
			actual, err := git.RevParse(ctx, bare, "refs/wi/pins/"+sha+"^{commit}")
			must(err)
			if actual != sha {
				panic("commit pin mismatch")
			}
		}
	}
	for _, i := range p.Items {
		_, err := store.NormalizeManifest(i.After)
		must(err)
		if i.Snapshot == nil {
			continue
		}
		must(git.VerifyDirtySnapshot(ctx, i.Source, *i.Snapshot))
		claim := i.After.Checkout.Claim
		if claim == nil || claims[claim.ID] || paths[claim.Path] || claim.ID == i.Before.Checkout.Claim.ID || claim.Generation != i.Before.Checkout.ClaimGeneration+1 {
			panic("invalid replacement claim")
		}
		claims[claim.ID] = true
		paths[claim.Path] = true
		must(git.VerifyDirtySnapshot(ctx, claim.Path, *i.Snapshot))
		for name, expected := range i.DirectoryDigests {
			for _, root := range []string{i.Source, claim.Path} {
				actual, err := directoryDigest(filepath.Join(root, name))
				must(err)
				if actual != expected {
					panic("opaque directory differs: " + root + "/" + name)
				}
			}
		}
	}
}
func convert(path string, p Plan) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	must(err)
	defer db.Close()
	must(db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		if number(meta.Get([]byte("schema_version"))) != 4 {
			return fmt.Errorf("expected schema 4")
		}
		if tx.Bucket([]byte("pending_native_writes")).Stats().KeyN != 0 {
			return fmt.Errorf("pending native writes must be drained")
		}
		projections := tx.Bucket([]byte("projections"))
		manifests := projections.Bucket([]byte(coordinator.ManifestProjection))
		if manifests.Stats().KeyN != len(p.Items) {
			return fmt.Errorf("item inventory changed")
		}
		sequence := number(meta.Get([]byte("global_sequence")))
		appendEvent := func(id, itemID, kind string, revision uint64, payload any) error {
			sequence++
			body, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			event := coordinator.DomainEvent{Sequence: sequence, ID: id, ItemID: itemID, ItemRevision: revision, Type: kind, Timestamp: p.CreatedAt, Actor: "operator", CausationID: "owned-repositories-schema-5", Payload: body}
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if tx.Bucket([]byte("domain_event_ids")).Get([]byte(id)) != nil {
				return fmt.Errorf("migration event already exists")
			}
			if err := tx.Bucket([]byte("domain_events")).Put(uintBytes(sequence), encoded); err != nil {
				return err
			}
			return tx.Bucket([]byte("domain_event_ids")).Put([]byte(id), uintBytes(sequence))
		}
		repositories, err := projections.CreateBucketIfNotExists([]byte(coordinator.RepositoryProjection))
		if err != nil {
			return err
		}
		for _, r := range p.Repositories {
			b, _ := json.Marshal(r.Record.OwnedRepository)
			if err := repositories.Put([]byte(r.Record.ID), b); err != nil {
				return err
			}
			if err := appendEvent("owned-repository:"+r.Record.ID, "", "repository.onboarded", 0, map[string]any{"repository_id": r.Record.ID, "name": r.Record.Name, "source": r.Source, "integration_branch": r.Record.IntegrationBranch}); err != nil {
				return err
			}
		}
		for _, i := range p.Items {
			var current model.Manifest
			if err := json.Unmarshal(manifests.Get([]byte(i.Before.ID)), &current); err != nil {
				return err
			}
			if !reflect.DeepEqual(current, i.Before) {
				return fmt.Errorf("canonical item changed since inventory: %s", i.Before.ID)
			}
			b, err := json.Marshal(i.After)
			if err != nil {
				return err
			}
			if err := manifests.Put([]byte(i.After.ID), b); err != nil {
				return err
			}
			revisions := tx.Bucket([]byte("item_revisions"))
			revision := number(revisions.Get([]byte(i.After.ID))) + 1
			if err := revisions.Put([]byte(i.After.ID), uintBytes(revision)); err != nil {
				return err
			}
			if err := appendEvent("owned-repository-item:"+i.After.ID, i.After.ID, "repository.schema_migrated", revision, map[string]any{"repository_id": i.After.Repository.ID, "previous_repository": i.Before.Repository, "previous_checkout": i.Before.Checkout, "checkout": i.After.Checkout}); err != nil {
				return err
			}
		}
		// Drop observations and old process/terminal ownership. Native history and
		// all source offsets, command outcomes, import digests and events remain.
		drop := []string{coordinator.RuntimeOwnershipProjection, coordinator.TerminalOwnershipProjection, coordinator.AgentObservationProjection, "worktree_observation", coordinator.RuntimeActivityProjection}
		for _, name := range drop {
			if projections.Bucket([]byte(name)) != nil {
				if err := projections.DeleteBucket([]byte(name)); err != nil {
					return err
				}
			}
		}
		if err := meta.Put([]byte("global_sequence"), uintBytes(sequence)); err != nil {
			return err
		}
		return meta.Put([]byte("schema_version"), uintBytes(5))
	}))
	// Check the converted aggregate references using the normal model decoder.
	must(db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("projections")).Bucket([]byte(coordinator.ManifestProjection))
		ids := []string{}
		for _, i := range p.Items {
			ids = append(ids, i.After.ID)
		}
		sort.Strings(ids)
		for _, id := range ids {
			var m model.Manifest
			if err := json.Unmarshal(b.Get([]byte(id)), &m); err != nil {
				return err
			}
			if m.Repository.ID == "" || m.Checkout.Specification.Kind != model.WorkspaceKindManagedSlot {
				return fmt.Errorf("invalid migrated item %s", id)
			}
		}
		return nil
	}))
}
