package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/repofiles"
	"github.com/regb/workitem/internal/usage"
)

// GeneralSettings is what the settings page shows about this installation.
type GeneralSettings struct {
	Version      string         `json:"version"`
	DefaultAgent string         `json:"default_agent"`
	Agents       []AgentProfile `json:"agents"`
	DataRoot     string         `json:"data_root"`
}

// Repository is a registered repository with the files Wi copies into its
// checkouts (paths and sizes, never content).
type Repository struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	IntegrationBranch string           `json:"integration_branch"`
	CreatedAt         time.Time        `json:"created_at"`
	Remote            *Remote          `json:"remote,omitempty"`
	Files             []repofiles.File `json:"files"`
}

// Remote is the upstream a repository follows, without any credentials its
// URL held. WebURL is its page on a known Git host, and Host names that
// host: github, gitlab, bitbucket, or empty for any other.
type Remote struct {
	URL    string `json:"url"`
	Branch string `json:"branch"`
	WebURL string `json:"web_url,omitempty"`
	Host   string `json:"host,omitempty"`
}

// Environment is a repository's environment variables, which Wi sets for
// what it starts in the repository's checkouts. Revision fences a change
// made from an older read.
type Environment struct {
	Project  Project        `json:"project"`
	Vars     []model.EnvVar `json:"vars"`
	Revision uint64         `json:"revision"`
	// Agents are the agents running in the repository's checkouts. Stale
	// ones started before the variables last changed and run with the
	// previous ones until they restart.
	Agents []EnvironmentAgent `json:"agents"`
}

type EnvironmentAgent struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"started_at"`
	Busy      bool      `json:"busy"`
	Stale     bool      `json:"stale"`
}

// EnvironmentRequest replaces a repository's variables, based on the
// revision the page read.
type EnvironmentRequest struct {
	Vars     []model.EnvVar `json:"vars"`
	Revision uint64         `json:"revision"`
}

func validateEnvironment(request *EnvironmentRequest) error {
	if len(request.Vars) > 500 {
		return errors.New("a repository holds at most 500 variables")
	}
	return model.ValidEnv(request.Vars)
}

func (s *Server) environment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	if !model.ValidID(project) {
		badRequest(w, errors.New("valid project required"))
		return
	}
	value, err := s.backend.Environment(r.Context(), project)
	reply(w, value, err)
}

func (s *Server) setEnvironment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	if !model.ValidID(project) {
		badRequest(w, errors.New("valid project required"))
		return
	}
	var request EnvironmentRequest
	if decodeValid(w, r, &request, 1<<20, validateEnvironment) {
		outcome(w, s.backend.SetEnvironment(r.Context(), project, request))
	}
}

// AddRepositoryRequest names a new repository and where to import it from:
// a Git URL, or a checkout on the machine Wi runs on. Without Branch, the
// source's default branch is the integration branch.
type AddRepositoryRequest struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Branch string `json:"branch,omitempty"`
}

func validateAddRepository(request *AddRepositoryRequest) error {
	request.Name, request.Source, request.Branch = strings.TrimSpace(request.Name), strings.TrimSpace(request.Source), strings.TrimSpace(request.Branch)
	if !model.ValidRepositoryName(request.Name) {
		return errors.New("a repository name uses letters, digits, '.', '_' or '-'")
	}
	if request.Source == "" || len(request.Source) > 2048 || len(request.Branch) > 256 {
		return errors.New("a source (a Git URL or a path) is required")
	}
	// The page has no current directory: a path must be absolute.
	if !strings.Contains(request.Source, ":") && !strings.HasPrefix(request.Source, "/") {
		return errors.New("the source must be a Git URL or an absolute path on this machine")
	}
	return nil
}

func (s *Server) addRepository(w http.ResponseWriter, r *http.Request) {
	var request AddRepositoryRequest
	if decodeValid(w, r, &request, 8<<10, validateAddRepository) {
		outcome(w, s.backend.AddRepository(r.Context(), request))
	}
}

// AgentProfiles is every agent profile, the default one, and for each how
// many open items use it.
type AgentProfiles struct {
	Revision uint64             `json:"revision"`
	Default  string             `json:"default"`
	Profiles []AgentProfileView `json:"profiles"`
}

type AgentProfileView struct {
	model.AgentProfile
	Users int `json:"users"`
}

// AgentProfileChange is one change, as wi agent profile makes: Put adds or
// replaces a profile, Remove deletes one, Default makes one the default.
type AgentProfileChange struct {
	Revision uint64              `json:"revision"`
	Put      *model.AgentProfile `json:"put,omitempty"`
	Remove   string              `json:"remove,omitempty"`
	Default  string              `json:"default,omitempty"`
}

func validateAgentProfileChange(change *AgentProfileChange) error {
	if change.Put != nil {
		return change.Put.Validate()
	}
	if change.Remove == "" && change.Default == "" {
		return errors.New("a change adds, removes, or sets the default profile")
	}
	return nil
}

// RepositorySetup is how a repository's checkouts are set up: its setup
// command, whether the command it declares in .config/wi.toml runs, how long
// setup may take, and which repository files are not copied.
type RepositorySetup struct {
	Revision       uint64   `json:"revision"`
	Run            string   `json:"run"`
	Trust          string   `json:"trust"`
	TimeoutSeconds int64    `json:"timeout_seconds"`
	Skip           []string `json:"skip"`
	// Declared is the command the repository's default branch declares.
	Declared string `json:"declared,omitempty"`
}

// RepositoryFileRequest adds a file (with Content) or removes one.
type RepositoryFileRequest struct {
	Path    string `json:"path"`
	Content []byte `json:"content,omitempty"`
}

func validateRepositorySetup(setup *RepositorySetup) error {
	if setup.Trust != "never" && setup.Trust != "always" {
		return errors.New("trust is never or always")
	}
	if len(setup.Run) > 4096 || len(setup.Skip) > repofiles.MaxFiles {
		return errors.New("setup settings are too long")
	}
	return nil
}

func validateRepositoryFile(request *RepositoryFileRequest) error {
	if len(request.Content) > repofiles.MaxBytes {
		return errors.New("a repository file holds at most 4 MiB")
	}
	return repofiles.ValidPath(request.Path)
}

// projectMutation decodes a change to a project, by its ID.
func projectMutation[T any](limit int64, validate func(*T) error, act func(context.Context, string, T) Outcome) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project := r.PathValue("id")
		if !model.ValidID(project) {
			badRequest(w, errors.New("valid project required"))
			return
		}
		var request T
		if decodeValid(w, r, &request, limit, validate) {
			outcome(w, act(r.Context(), project, request))
		}
	}
}

// Usage is what the usage plugin reports: how much of each subscription's
// quota is left. Enabled is false while the plugin is not enabled.
type Usage struct {
	Enabled bool                `json:"enabled"`
	Results []usage.QuotaResult `json:"results"`
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	value, err := s.backend.Usage(r.Context(), r.URL.Query().Get("refresh") == "1")
	reply(w, value, err)
}
