package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	DescriptionFilename = "DESCRIPTION.md"

	StateBacklog  = "backlog"
	StateWorking  = "working"
	StateWaiting  = "waiting"
	StateArchived = "archived"

	CheckoutPresent = "present"
	CheckoutAbsent  = "absent"

	WorkspaceKindManagedSlot    = "managed-slot"
	WorkspaceKindRepositoryHome = "repository-home"
	WorkspaceKindExecutor       = "executor"
)

type Manifest struct {
	ID             string    `json:"id"`
	Slug           string    `json:"slug"`
	Title          string    `json:"title"`
	State          string    `json:"state"`
	StateChangedAt time.Time `json:"state_changed_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	Repository          Repository    `json:"repository"`
	Checkout            Checkout      `json:"checkout"`
	PrimaryConversation *Conversation `json:"primary_conversation,omitempty"`
	Agent               string        `json:"agent,omitempty"`
	Labels              []string      `json:"labels"`
	DeepWork            bool          `json:"deep_work"`
}

func (m Manifest) TerminalSessionName() string {
	return TerminalSessionName(m.ID)
}

type Repository struct {
	RootAtCreation    string `json:"root_at_creation"`
	GitCommonDir      string `json:"git_common_dir"`
	RemoteURL         string `json:"remote_url"`
	CreatedFromCommit string `json:"created_from_commit"`
	CurrentRoot       string `json:"current_root,omitempty"`
}

// OperationalRoot returns the repository checkout used for current Git
// operations. RootAtCreation remains immutable provenance when a repository is
// relocated.
func (r Repository) OperationalRoot() string {
	if root := strings.TrimSpace(r.CurrentRoot); root != "" {
		return root
	}
	return strings.TrimSpace(r.RootAtCreation)
}

func RepositoryKey(repository Repository) string {
	value := strings.TrimSpace(repository.GitCommonDir)
	if value == "" {
		value = strings.TrimSpace(repository.RemoteURL)
	}
	if value == "" {
		value = strings.TrimSpace(repository.RootAtCreation)
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}

type Checkout struct {
	Specification   CheckoutSpecification `json:"specification"`
	ClaimGeneration uint64                `json:"claim_generation"`
	Claim           *WorkspaceClaim       `json:"claim"`
}

type CheckoutSpecification struct {
	Kind            string `json:"kind"`
	ExecutorProfile string `json:"executor_profile,omitempty"`
	Branch          string `json:"branch"`
}

type WorkspaceClaim struct {
	ID              string         `json:"id"`
	Generation      uint64         `json:"generation"`
	ExecutorProfile string         `json:"executor_profile,omitempty"`
	Executor        *ExecutorClaim `json:"executor,omitempty"`
	RepositoryKey   string         `json:"repository_key,omitempty"`
	Slot            string         `json:"slot,omitempty"`
	Path            string         `json:"path"`
}

func NewCheckout(kind, profile, branch string) Checkout {
	return Checkout{Specification: CheckoutSpecification{Kind: kind, ExecutorProfile: profile, Branch: branch}}
}

func (c *Checkout) AssignNewClaim(profile, repositoryKey, slot, path string) (*WorkspaceClaim, error) {
	if c.Claim != nil {
		return nil, fmt.Errorf("workspace already has claim %s", c.Claim.ID)
	}
	if c.ClaimGeneration == ^uint64(0) {
		return nil, fmt.Errorf("workspace claim generation exhausted")
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	claim := &WorkspaceClaim{ID: id, Generation: c.ClaimGeneration + 1, ExecutorProfile: profile, RepositoryKey: repositoryKey, Slot: slot, Path: path}
	c.ClaimGeneration = claim.Generation
	c.Claim = claim
	return claim, nil
}

func (c Checkout) EverMaterialized() bool { return c.ClaimGeneration > 0 }

func (c Checkout) Present() bool {
	return c.Claim != nil && strings.TrimSpace(c.Claim.Path) != ""
}

func (c Checkout) Presence() string {
	if c.Present() {
		return CheckoutPresent
	}
	return CheckoutAbsent
}

func (c *Checkout) ClearClaim() {
	c.Claim = nil
}

// ExecutorClaim pins the environment selected before Docker or SSH is called.
// A failed operation retains this record for retry or explicit release.
type ExecutorClaim struct {
	Name         string `json:"name"`
	Driver       string `json:"driver"`
	Host         string `json:"host,omitempty"`
	Root         string `json:"root,omitempty"`
	SSHConfig    string `json:"ssh_config,omitempty"`
	Image        string `json:"image"`
	Network      string `json:"network"`
	Ready        bool   `json:"ready"`
	Releasing    bool   `json:"releasing"`
	Destroying   bool   `json:"destroying"`
	ExportedHead string `json:"exported_head,omitempty"`
}

type Conversation struct {
	Driver  string `json:"driver,omitempty"`
	ID      string `json:"id"`
	Locator string `json:"locator,omitempty"`
	Format  string `json:"format,omitempty"`
}

func (c Conversation) NativeLocator() string { return c.Locator }

// NewManifest returns a manifest populated with stable defaults.
func NewManifest(id, slug, title string, labels []string, deepWork bool, now time.Time, repo Repository, checkout Checkout) Manifest {
	if checkout.Specification.Kind == "" {
		checkout.Specification.Kind = WorkspaceKindManagedSlot
	}
	if strings.TrimSpace(checkout.Specification.Branch) == "" {
		checkout.Specification.Branch = ItemBranchName(slug, id)
	}
	normalizedLabels, err := NormalizeLabels(labels)
	if err != nil {
		normalizedLabels = []string{}
	}
	return Manifest{
		ID:                  id,
		Slug:                slug,
		Title:               title,
		State:               StateBacklog,
		StateChangedAt:      now,
		CreatedAt:           now,
		UpdatedAt:           now,
		Repository:          repo,
		Checkout:            checkout,
		PrimaryConversation: nil,
		Agent:               "default",
		Labels:              normalizedLabels,
		DeepWork:            deepWork,
	}
}
