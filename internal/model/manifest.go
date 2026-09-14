package model

import (
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

type Checkout struct {
	Kind     string  `json:"kind"`
	Executor string  `json:"executor"`
	Slot     string  `json:"slot,omitempty"`
	Path     *string `json:"path"`
	Branch   string  `json:"branch"`
}

func (c Checkout) Present() bool {
	return c.Path != nil && strings.TrimSpace(*c.Path) != ""
}

func (c Checkout) Presence() string {
	if c.Present() {
		return CheckoutPresent
	}
	return CheckoutAbsent
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
	if checkout.Kind == "" {
		checkout.Kind = WorkspaceKindManagedSlot
	}
	if checkout.Executor == "" {
		checkout.Executor = ExecutorLocal
	}
	if strings.TrimSpace(checkout.Branch) == "" {
		checkout.Branch = ItemBranchName(slug, id)
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
