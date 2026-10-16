package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	DescriptionFilename = "DESCRIPTION.md"

	// Lifecycle states describe resource ownership only. Whether an active
	// item can make progress is expressed by blockers, and how it ended by a
	// resolution recorded when it closes.
	StateBacklog = "backlog"
	StateActive  = "active"
	StateClosed  = "closed"

	CheckoutPresent = "present"
	CheckoutAbsent  = "absent"

	WorkspaceKindManagedSlot = "managed-slot"
	// LegacyWorkspaceKindRepositoryHome is used only by offline migration tools.
	LegacyWorkspaceKindRepositoryHome = "repository-home"

	ExecutorLocal = "local"
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
	Resolution          string        `json:"resolution,omitempty"`
	Blockers            []Blocker     `json:"blockers,omitempty"`
}

func (m Manifest) TerminalSessionName() string {
	return TerminalSessionName(m.ID)
}

type Repository struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"-"`
	IntegrationBranch string `json:"-"`
	RootAtCreation    string `json:"root_at_creation"`
	GitCommonDir      string `json:"git_common_dir"`
	RemoteURL         string `json:"remote_url"`
	CreatedFromCommit string `json:"created_from_commit"`
	CurrentRoot       string `json:"current_root,omitempty"`
}

// MarshalJSON persists only repository identity and immutable item provenance for
// registered repositories. Operational fields are resolved, never copied intent.
func (r Repository) MarshalJSON() ([]byte, error) {
	if r.ID != "" {
		return json.Marshal(struct {
			ID     string `json:"id"`
			Commit string `json:"created_from_commit"`
		}{r.ID, r.CreatedFromCommit})
	}
	type legacy Repository
	return json.Marshal(legacy(r))
}

func (r *Repository) UnmarshalJSON(data []byte) error {
	type decoded Repository
	var value decoded
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.ID != "" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		for _, key := range []string{"root_at_creation", "git_common_dir", "remote_url", "current_root"} {
			if _, ok := fields[key]; ok {
				return fmt.Errorf("registered repository references must not contain %s", key)
			}
		}
	}
	*r = Repository(value)
	return nil
}

// OperationalRoot returns the resolved Git location used for operations. For
// registered repositories this is the owned bare store; immutable provenance is
// the repository ID and CreatedFromCommit, not the hydrated filesystem paths.
func (r Repository) OperationalRoot() string {
	if root := strings.TrimSpace(r.CurrentRoot); root != "" {
		return root
	}
	return strings.TrimSpace(r.RootAtCreation)
}

func RepositoryKey(repository Repository) string {
	if repository.ID != "" {
		return repository.ID
	}
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
	Kind     string `json:"kind"`
	Executor string `json:"executor"`
	Branch   string `json:"branch"`
}

type WorkspaceClaim struct {
	ID            string `json:"id"`
	Generation    uint64 `json:"generation"`
	Executor      string `json:"executor"`
	RepositoryKey string `json:"repository_key,omitempty"`
	Slot          string `json:"slot,omitempty"`
	Path          string `json:"path"`
}

// UnmarshalJSON rejects obsolete flat records rather than silently dropping
// checkout intent. Local data conversion is an offline, one-time operation.
func (c *Checkout) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if spec, ok := fields["specification"]; !ok || string(spec) == "null" {
		return fmt.Errorf("checkout specification is required")
	}
	for _, key := range []string{"kind", "branch", "path", "executor"} {
		if _, ok := fields[key]; ok {
			return fmt.Errorf("obsolete flat checkout field %q", key)
		}
	}
	type current Checkout
	var decoded current
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = Checkout(decoded)
	return nil
}

func NewCheckout(kind, executor, branch string) Checkout {
	return Checkout{Specification: CheckoutSpecification{Kind: kind, Executor: executor, Branch: branch}}
}

func (c *Checkout) AssignNewClaim(executor, repositoryKey, slot, path string) (*WorkspaceClaim, error) {
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
	claim := &WorkspaceClaim{ID: id, Generation: c.ClaimGeneration + 1, Executor: executor, RepositoryKey: repositoryKey, Slot: slot, Path: path}
	c.ClaimGeneration = claim.Generation
	c.Claim = claim
	return claim, nil
}

func (c *Checkout) AcceptClaim(claim WorkspaceClaim) error {
	if c.Claim != nil {
		return fmt.Errorf("workspace already has claim %s", c.Claim.ID)
	}
	if claim.Generation == 0 || claim.Generation != c.ClaimGeneration+1 {
		return fmt.Errorf("workspace claim generation %d does not follow %d", claim.Generation, c.ClaimGeneration)
	}
	c.ClaimGeneration = claim.Generation
	c.Claim = &claim
	return nil
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

type Conversation struct {
	Driver   string `json:"driver,omitempty"`
	Executor string `json:"executor,omitempty"`
	ID       string `json:"id"`
	Locator  string `json:"locator,omitempty"`
	Format   string `json:"format,omitempty"`
}

func (c Conversation) NativeLocator() string { return c.Locator }

// NewManifest returns a manifest populated with stable defaults.
func NewManifest(id, slug, title string, labels []string, deepWork bool, now time.Time, repo Repository, checkout Checkout) Manifest {
	if checkout.Specification.Kind == "" {
		checkout.Specification.Kind = WorkspaceKindManagedSlot
	}
	if checkout.Specification.Executor == "" {
		checkout.Specification.Executor = ExecutorLocal
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
