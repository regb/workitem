// Package web is a local access mechanism. It does not own Wi policy or state.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

// dist/app is the React client built from web/ (see scripts/install-local).
// dist/placeholder.html is served when the client was not built.
//
//go:embed all:dist
var dist embed.FS

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type AgentProfile struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// ListItem is one dashboard row: what the page shows or decides on. It leaves
// out bulky and volatile fields, such as manifests and activity ages, so an
// unchanged list encodes identically and is not sent again.
type ListItem struct {
	ID        string          `json:"id"`
	Slug      string          `json:"slug"`
	Title     string          `json:"title"`
	State     string          `json:"state"`
	Labels    []string        `json:"labels"`
	Agent     *ListAgent      `json:"agent,omitempty"`
	Worktree  *ListWorktree   `json:"worktree,omitempty"`
	Blockers  []model.Blocker `json:"blockers,omitempty"`
	Unread    bool            `json:"unread,omitempty"`
	ProjectID string          `json:"project_id"`
}
type ListAgent struct {
	Status string `json:"status"`
}
type ListWorktree struct {
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Dirty          bool   `json:"dirty,omitempty"`
	BranchMismatch bool   `json:"branch_mismatch,omitempty"`
}
type Section struct {
	Name  string     `json:"name"`
	Items []ListItem `json:"items"`
}

// List is the dashboard. Deep work stays in the data model but is not shown
// in the browser yet.
type List struct {
	Warnings        []string  `json:"warnings"`
	DisplaySections []Section `json:"display_sections"`
	Projects        []Project `json:"projects"`
}
type Chat struct {
	Startup     *Creation                    `json:"startup,omitempty"`
	Description string                       `json:"description"`
	Project     Project                      `json:"project"`
	Item        model.Manifest               `json:"item"`
	Runtime     app.AgentRuntimeStatusResult `json:"runtime"`
	Status      string                       `json:"status"`
	// StatusReason says why, for a problem status or a pending question.
	StatusReason string `json:"status_reason,omitempty"`
	// Asking means the agent paused its turn on a question, plan approval or
	// permission prompt that is answered in its terminal.
	Asking   bool                   `json:"asking,omitempty"`
	History  driverprotocol.History `json:"history"`
	Control  *ControlView           `json:"control,omitempty"`
	Unread   bool                   `json:"unread"`
	Warnings []string               `json:"warnings"`
}

// ControlView is the controller lease as seen by one browser tab. You is true
// when this tab holds control; Plan describes what taking control would do.
type ControlView struct {
	Status app.ControlStatus `json:"status"`
	You    bool              `json:"you"`
	Plan   app.ControlPlan   `json:"plan"`
}

type TakeControl struct {
	Confirm bool `json:"confirm"`
}

// BlockRequest adds one blocker. Until is a duration or time accepted by
// `wi wait --until`, used for time blockers.
type BlockRequest struct {
	Kind   string `json:"kind"`
	Until  string `json:"until,omitempty"`
	ItemID string `json:"item_id,omitempty"`
	Event  string `json:"event,omitempty"`
}

// SettingsRequest changes an item's agent settings; an empty value resets
// one to the profile. Restart allows restarting a runtime that cannot apply
// a change live.
type SettingsRequest struct {
	Settings map[string]string `json:"settings"`
	Restart  bool              `json:"restart,omitempty"`
}

type ClearBlockerRequest struct {
	BlockerID string `json:"blocker_id"`
}

type ReadRequest struct {
	Unread bool `json:"unread"`
}

// CloseRequest closes an item. Force aborts a busy agent's turn so it can be
// stopped; the browser sends it only after the operator confirms.
type CloseRequest struct {
	Resolution string `json:"resolution"`
	Force      bool   `json:"force,omitempty"`
}

type Create struct {
	ProjectID string `json:"project_id"`
	Agent     string `json:"agent,omitempty"`
	Prompt    string `json:"prompt"`
}

// BacklogCreate saves an item to the backlog without starting an agent.
type BacklogCreate struct {
	ProjectID   string `json:"project_id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}
type Message struct {
	Text      string                        `json:"text"`
	Images    []driverprotocol.ImageContent `json:"images,omitempty"`
	Delivery  string                        `json:"delivery"`
	RuntimeID string                        `json:"runtime_id"`
	// ClientID is the page's identity for the message. Drivers that can link
	// a prompt to its history entry report it back on the content stream.
	ClientID string `json:"client_id,omitempty"`
	// Approval answers an approval the previous attempt asked for: "allow"
	// or "deny". Without it, starting an agent that needs one asks.
	Approval string `json:"approval,omitempty"`
}

// StartRequest starts a backlog item with the configured startup message.
type StartRequest struct {
	ClientID string `json:"client_id,omitempty"`
	Approval string `json:"approval,omitempty"`
}

// ApprovalRequest is a question starting an agent needs answered, such as
// trusting a worktree's .envrc. The page asks and resends with the answer.
type ApprovalRequest struct {
	Question string `json:"question"`
	Path     string `json:"path,omitempty"`
}

// Outcome reports a mutation. Error refuses or fails it; Observed is the
// durable state read afterwards, so retained partial outcomes stay visible.
type Outcome struct {
	CreationUncertain bool             `json:"creation_uncertain,omitempty"`
	Result            any              `json:"result,omitempty"`
	Error             string           `json:"error,omitempty"`
	Warnings          []string         `json:"warnings,omitempty"`
	Observed          *model.Manifest  `json:"observed,omitempty"`
	Control           *ControlView     `json:"control,omitempty"`
	Approval          *ApprovalRequest `json:"approval,omitempty"`
	// Revision is the created item's revision when it was queued to start,
	// which the start is fenced on. The page never sees it.
	Revision uint64 `json:"-"`
}

// maxFileQuery bounds the fuzzy query a page sends for an @-mention. The
// search itself is the backend's: Git lists the files and fzf ranks them.
const maxFileQuery = 200

// ErrNotFound reports that no work item has an ID the page sent.
var ErrNotFound = errors.New("no work item has this ID; it may have been deleted")

// Backend is the Wi composition the page drives. It owns all policy.
type Backend interface {
	// CheckItem reports ErrNotFound unless an item has exactly this ID. Wi
	// resolves selectors loosely (prefixes, slugs, titles, descriptions); a
	// page names items by ID only, and a missing one must not resolve to
	// another item, say one whose description mentions it.
	CheckItem(ctx context.Context, id string) error
	Projects(context.Context) ([]Project, error)
	Agents(context.Context) ([]AgentProfile, error)
	// List reads the work list. With inspect, the daemon first re-observes
	// runtimes, worktrees and conversations; without, it answers from what
	// it already observed.
	List(ctx context.Context, inspect bool) (List, error)
	Next(ctx context.Context, current string, projects []string) (string, error)
	Create(context.Context, Create) Outcome
	CreateBacklog(context.Context, BacklogCreate) Outcome
	// StartCreated starts an item Create made, unless it changed since
	// revision, when it was queued.
	StartCreated(ctx context.Context, id string, revision uint64) Outcome
	Chat(ctx context.Context, id string) (Chat, error)
	History(ctx context.Context, id, leaf, before string) (driverprotocol.History, error)
	Content(ctx context.Context, id string) (driverprotocol.Content, error)
	// Changes waits up to wait for items to change after a revision of the
	// daemon instance epoch; see coordinator.ChangesRequest.
	Changes(ctx context.Context, epoch string, after uint64, wait time.Duration) (Changes, error)
	// ContentStream follows live content until the stream ends; see
	// app.AgentContentStream.
	ContentStream(ctx context.Context, id string, apply func(driverprotocol.ContentEvent) error) error
	Send(ctx context.Context, id string, message Message) Outcome
	TakeControl(ctx context.Context, id string, request TakeControl) Outcome
	// Files fuzzy-finds paths in the item's worktree for an @-mention.
	Files(ctx context.Context, id, query string) ([]string, error)
	// ProjectFiles fuzzy-finds paths on a project's integration branch, for
	// @-mentions in new work.
	ProjectFiles(ctx context.Context, project, query string) ([]string, error)
	// Stop aborts the agent's current turn as this tab's controller. It never
	// starts a runtime. Confirm takes control from another client first.
	Stop(ctx context.Context, id string, request TakeControl) Outcome
	ReleaseControl(ctx context.Context, id string) Outcome
	Block(ctx context.Context, id string, request BlockRequest) Outcome
	ClearBlocker(ctx context.Context, id string, request ClearBlockerRequest) Outcome
	MarkRead(ctx context.Context, id string, request ReadRequest) Outcome
	// Start makes a backlog item active and sends its agent the configured
	// startup message, like wi new --start.
	Start(ctx context.Context, id string, request StartRequest) Outcome
	Settings(ctx context.Context, id string) (app.AgentSettingsView, error)
	SetSettings(ctx context.Context, id string, request SettingsRequest) Outcome
	Shelve(ctx context.Context, id string) Outcome
	Close(ctx context.Context, id string, request CloseRequest) Outcome
}

// Changes names the items that changed after a revision. All means every item
// may have changed.
type Changes struct {
	Epoch    string
	Revision uint64
	Items    []string
	All      bool
}

type clientKey struct{}

// ClientID returns the browser tab identity sent with the request. Control
// leases are held per tab so two open tabs do not share control.
func ClientID(ctx context.Context) string {
	value, _ := ctx.Value(clientKey{}).(string)
	return value
}

// Options configure access. The listener itself always stays on loopback.
type Options struct {
	// AccessURL is the browser-facing origin: the loopback listener, or a
	// trusted reverse proxy such as a private tailnet service.
	AccessURL string
	// Token replaces the random launch secret so the URL survives restarts.
	Token string
	// NoToken trusts the proxy to restrict access. Host and Origin checks remain.
	NoToken bool
}

type Server struct {
	backend    Backend
	host       string
	origin     string
	token      string
	noToken    bool
	streams    chan struct{}
	requests   chan struct{}
	background *backgroundStarts
	lists      *listHub
	contents   *contentFeeds
	mux        *http.ServeMux
}

func New(backend Backend, opts Options) (*Server, error) {
	parsed, err := url.Parse(opts.AccessURL)
	if err != nil {
		return nil, fmt.Errorf("invalid access URL: %w", err)
	}
	validScheme := parsed.Scheme == "http" || parsed.Scheme == "https"
	if !validScheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("access URL must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	token := opts.Token
	if token == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		token = hex.EncodeToString(b)
	} else if b, err := hex.DecodeString(token); err != nil || len(b) != 32 {
		return nil, fmt.Errorf("access token must be 64 hexadecimal characters")
	}
	s := &Server{backend: backend, host: parsed.Host, origin: parsed.Scheme + "://" + parsed.Host, token: token, noToken: opts.NoToken, streams: make(chan struct{}, 16), requests: make(chan struct{}, 16), background: newBackgroundStarts(backend), lists: newListHub(backend), contents: newContentFeeds(backend)}
	s.routes()
	return s, nil
}

func (s *Server) Close() { s.background.close() }

// Address is the page's address without the access secret.
func (s *Server) Address() string { return s.origin + "/" }

func (s *Server) URL() string {
	if s.noToken {
		return s.origin + "/"
	}
	return s.origin + "/#token=" + s.token
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("/", s.client())
	m.HandleFunc("GET /api/projects", read(s.backend.Projects))
	m.HandleFunc("GET /api/agents", read(s.backend.Agents))
	// A page load answers from the daemon's observations; the event stream
	// inspects again on its next beat.
	m.HandleFunc("GET /api/items", read(func(ctx context.Context) (List, error) { return s.backend.List(ctx, false) }))
	m.HandleFunc("GET /api/creations", func(w http.ResponseWriter, r *http.Request) { reply(w, s.background.list(), nil) })
	m.HandleFunc("GET /api/next", s.next)
	m.HandleFunc("GET /api/events", s.events)
	m.HandleFunc("POST /api/items", s.create)
	m.HandleFunc("POST /api/backlog", s.createBacklog)

	m.HandleFunc("GET /api/items/{id}/chat", s.item(s.chat))
	m.HandleFunc("GET /api/items/{id}/history", s.item(s.history))
	m.HandleFunc("GET /api/items/{id}/files", s.item(s.files))
	m.HandleFunc("GET /api/projects/{id}/files", s.projectFiles)
	m.HandleFunc("POST /api/items/{id}/messages", s.item(s.withClient(mutate(12<<20, validateMessage, s.backend.Send))))
	m.HandleFunc("POST /api/items/{id}/control", s.item(s.withClient(mutate(0, nil, s.backend.TakeControl))))
	m.HandleFunc("POST /api/items/{id}/stop", s.item(s.withClient(mutate(0, nil, s.backend.Stop))))
	m.HandleFunc("POST /api/items/{id}/release", s.item(s.withClient(mutate(0, nil, func(ctx context.Context, id string, _ struct{}) Outcome { return s.backend.ReleaseControl(ctx, id) }))))
	m.HandleFunc("POST /api/items/{id}/blockers", s.item(mutate(0, validateBlock, s.backend.Block)))
	m.HandleFunc("POST /api/items/{id}/clear-blocker", s.item(mutate(0, validateClearBlocker, s.backend.ClearBlocker)))
	m.HandleFunc("POST /api/items/{id}/read", s.item(mutate(0, nil, s.backend.MarkRead)))
	m.HandleFunc("GET /api/items/{id}/settings", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		value, err := s.backend.Settings(r.Context(), id)
		reply(w, value, err)
	}))
	m.HandleFunc("POST /api/items/{id}/settings", s.item(mutate(0, validateSettings, s.backend.SetSettings)))
	m.HandleFunc("POST /api/items/{id}/start", s.item(s.withClient(mutate(1<<10, validateStart, s.backend.Start))))
	m.HandleFunc("POST /api/items/{id}/shelve", s.item(mutate(0, nil, func(ctx context.Context, id string, _ struct{}) Outcome { return s.backend.Shelve(ctx, id) })))
	m.HandleFunc("POST /api/items/{id}/close", s.item(mutate(0, validateClose, s.backend.Close)))
	s.mux = m
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Scripts come only from the embedded bundle. Inline styles are allowed
	// for component positioning and syntax highlighting; agent HTML is never
	// rendered, so this does not admit agent-controlled markup.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if r.Host != s.host {
		http.Error(w, "invalid Host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	// Refuse rather than redirect paths that are not already canonical.
	if path.Clean(r.URL.Path) != r.URL.Path && r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		var release func()
		if r, release = s.admitAPI(w, r); r == nil {
			return
		}
		defer release()
	}
	s.mux.ServeHTTP(w, r)
}

// admitAPI authenticates an API request and bounds it. It returns nil when it
// has already answered the request.
func (s *Server) admitAPI(w http.ResponseWriter, r *http.Request) (*http.Request, func()) {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return nil, nil
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.noToken && subtle.ConstantTimeCompare([]byte(auth), []byte(s.token)) != 1 {
		http.Error(w, "open the launch URL printed by wi web", http.StatusUnauthorized)
		return nil, nil
	}
	if r.Method == http.MethodPost && (r.Header.Get("Origin") != s.origin || r.Header.Get("Content-Type") != "application/json") {
		http.Error(w, "same-origin JSON required", http.StatusForbidden)
		return nil, nil
	}
	if client := strings.TrimSpace(r.Header.Get("X-Wi-Client")); client != "" {
		if len(client) > 64 || strings.Trim(client, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			http.Error(w, "invalid client identity", http.StatusBadRequest)
			return nil, nil
		}
		r = r.WithContext(context.WithValue(r.Context(), clientKey{}, client))
	}
	if r.URL.Path == "/api/events" {
		return r, func() {} // Streams have their own bound and lifetime.
	}
	select {
	case s.requests <- struct{}{}:
	default:
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return nil, nil
	}
	// Taking control, sending with an implicit takeover, starting or changing
	// settings can stop one runtime and wait for another to become ready on
	// the conversation; reading settings may run the agent's model catalog.
	timeout := 15 * time.Second
	for _, slow := range []string{"/control", "/messages", "/start", "/settings"} {
		if strings.HasSuffix(r.URL.Path, slow) && (r.Method == http.MethodPost || slow == "/settings") {
			timeout = 45 * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + 5*time.Second))
	return r.WithContext(ctx), func() { cancel(); <-s.requests }
}

func read[T any](get func(context.Context) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		value, err := get(r.Context())
		reply(w, value, err)
	}
}

// client serves the embedded React build. Hashed assets are cached; every
// other path is a client route and receives index.html. Unknown API paths
// are not found, whatever their method.
func (s *Server) client() http.HandlerFunc {
	app, err := fs.Sub(dist, "dist/app")
	if err == nil {
		if _, statErr := fs.Stat(app, "index.html"); statErr != nil {
			err = statErr
		}
	}
	if err != nil {
		placeholder, _ := dist.ReadFile("dist/placeholder.html")
		return func(w http.ResponseWriter, r *http.Request) {
			if !pageRequest(w, r) {
				return
			}
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(placeholder)
		}
	}
	index, _ := fs.ReadFile(app, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if !pageRequest(w, r) {
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" && name != "index.html" {
			if data, err := fs.ReadFile(app, name); err == nil {
				if typ := mime.TypeByExtension(path.Ext(name)); typ != "" {
					w.Header().Set("Content-Type", typ)
				}
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				_, _ = w.Write(data)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	}
}

// pageRequest answers requests that cannot be pages and reports whether the
// handler should serve one.
func pageRequest(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// item passes a validated item ID from the path to the handler.
func (s *Server) item(handle func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !model.ValidID(id) {
			http.NotFound(w, r)
			return
		}
		if err := s.backend.CheckItem(r.Context(), id); err != nil {
			reply(w, struct{}{}, err)
			return
		}
		handle(w, r, id)
	}
}

func (s *Server) withClient(handle func(http.ResponseWriter, *http.Request, string)) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		if ClientID(r.Context()) == "" {
			badRequest(w, errors.New("This browser tab has no client identity; reload the page."))
			return
		}
		handle(w, r, id)
	}
}

// mutate decodes one bounded JSON request, validates it, and reports the
// backend's outcome. A zero limit uses the default small-body bound.
func mutate[T any](limit int64, validate func(*T) error, act func(context.Context, string, T) Outcome) func(http.ResponseWriter, *http.Request, string) {
	if limit == 0 {
		limit = 128 << 10
	}
	return func(w http.ResponseWriter, r *http.Request, id string) {
		var request T
		if decodeValid(w, r, &request, limit, validate) {
			outcome(w, act(r.Context(), id, request))
		}
	}
}

// decodeValid decodes one bounded JSON request and validates it, which may
// also normalize it; a bad one is answered here, and it reports false.
func decodeValid[T any](w http.ResponseWriter, r *http.Request, request *T, limit int64, validate func(*T) error) bool {
	if err := decode(w, r, request, limit); err != nil {
		badRequest(w, err)
		return false
	}
	if validate != nil {
		if err := validate(request); err != nil {
			badRequest(w, err)
			return false
		}
	}
	return true
}

// validateCreate requires a registered project and a prompt; new work's
// title comes later, from the prompt.
func validateCreate(create *Create) error {
	create.Prompt = strings.TrimSpace(create.Prompt)
	create.Agent = strings.TrimSpace(create.Agent)
	if !model.ValidID(create.ProjectID) || len(create.Agent) > 128 || create.Prompt == "" || len(create.Prompt) > 64<<10 {
		return errors.New("project, configured agent, and prompt (up to 64 KiB) required")
	}
	return nil
}

// validateBacklog requires a registered project and a one-line title.
func validateBacklog(create *BacklogCreate) error {
	create.Title, create.Description = strings.TrimSpace(create.Title), strings.TrimSpace(create.Description)
	if !model.ValidID(create.ProjectID) || create.Title == "" || len(create.Title) > 200 || strings.ContainsAny(create.Title, "\r\n") || len(create.Description) > 64<<10 {
		return errors.New("project and a one-line title (up to 200 bytes) required; description up to 64 KiB")
	}
	return nil
}

// files answers an @-mention search with the best matching worktree paths.
func (s *Server) files(w http.ResponseWriter, r *http.Request, id string) {
	query := r.URL.Query().Get("q")
	if len(query) > maxFileQuery {
		badRequest(w, errors.New("file query is too long"))
		return
	}
	files, err := s.backend.Files(r.Context(), id, query)
	reply(w, map[string]any{"files": files}, err)
}

// projectFiles answers an @-mention search in new work with the best matching
// paths of the project's integration branch.
func (s *Server) projectFiles(w http.ResponseWriter, r *http.Request) {
	project, query := r.PathValue("id"), r.URL.Query().Get("q")
	if !model.ValidID(project) || len(query) > maxFileQuery {
		badRequest(w, errors.New("valid project and a short file query are required"))
		return
	}
	files, err := s.backend.ProjectFiles(r.Context(), project, query)
	reply(w, map[string]any{"files": files}, err)
}

func (s *Server) next(w http.ResponseWriter, r *http.Request) {
	current := r.URL.Query().Get("current")
	if current != "" && !model.ValidID(current) {
		badRequest(w, errors.New("invalid current item"))
		return
	}
	id, err := s.backend.Next(r.Context(), current, r.URL.Query()["project"])
	reply(w, map[string]string{"id": id}, err)
}

// createBacklog saves an item to the backlog. Nothing starts, so it is a
// plain request, not a background job.
func (s *Server) createBacklog(w http.ResponseWriter, r *http.Request) {
	var create BacklogCreate
	if !decodeValid(w, r, &create, 128<<10, validateBacklog) {
		return
	}
	out := s.backend.CreateBacklog(r.Context(), create)
	status := http.StatusCreated
	if out.Error != "" {
		status = http.StatusConflict
	}
	writeJSON(w, status, out)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var create Create
	if !decodeValid(w, r, &create, 128<<10, validateCreate) {
		return
	}
	// Reserve capacity before committing an item. A receipt means its prompt
	// is durable, not merely held in a browser or an in-memory queue.
	if !s.background.reserve() {
		writeJSON(w, http.StatusTooManyRequests, Outcome{Error: "Too many conversations starting. Try again shortly."})
		return
	}
	out := s.backend.Create(r.Context(), create)
	if out.Error != "" || out.Observed == nil {
		if out.Observed != nil {
			s.background.recordProblem(*out.Observed, out.Error, out.Warnings)
		}
		s.background.release()
		outcome(w, out)
		return
	}
	writeJSON(w, http.StatusAccepted, s.background.enqueue(*out.Observed, out.Revision, out.Warnings))
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request, id string) {
	value, err := s.chatSnapshot(r.Context(), id)
	reply(w, value, err)
}

// chatSnapshot reads a conversation, or only its startup receipt while a new
// item is still being started in the background.
func (s *Server) chatSnapshot(ctx context.Context, id string) (Chat, error) {
	job, known := s.background.lookup(id)
	if known && (job.State == "queued" || job.State == "starting") {
		return Chat{Item: model.Manifest{ID: id, Title: job.Title}, Startup: &job}, nil
	}
	value, err := s.backend.Chat(ctx, id)
	if known {
		value.Startup = &job
	}
	return value, err
}

func (s *Server) history(w http.ResponseWriter, r *http.Request, id string) {
	leaf, before := r.URL.Query().Get("leaf"), r.URL.Query().Get("before")
	if leaf == "" || before == "" || len(leaf) > 4096 || len(before) > 4096 {
		badRequest(w, errors.New("bounded branch and history cursor required"))
		return
	}
	value, err := s.backend.History(r.Context(), id, leaf, before)
	reply(w, value, err)
}

// messageClientID bounds a page's message identity, which becomes the
// command's request ID.
var messageClientID = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)

func validApproval(answer string) bool {
	return answer == "" || answer == "allow" || answer == "deny"
}

func validateStart(request *StartRequest) error {
	if !messageClientID.MatchString(request.ClientID) {
		return errors.New("start client id must be up to 64 letters, digits, dots, dashes or underscores")
	}
	if !validApproval(request.Approval) {
		return errors.New(`approval must be "allow" or "deny"`)
	}
	return nil
}

func validateMessage(message *Message) error {
	if len(message.Text) > 64<<10 || (strings.TrimSpace(message.Text) == "" && len(message.Images) == 0) || len(message.RuntimeID) > 200 || (message.Delivery != "prompt" && message.Delivery != "steer" && message.Delivery != "follow_up") {
		return errors.New("message or image and prompt, steer or follow_up delivery required")
	}
	if len(message.Images) > 4 {
		return errors.New("at most four images are allowed")
	}
	if !messageClientID.MatchString(message.ClientID) {
		return errors.New("message client id must be up to 64 letters, digits, dots, dashes or underscores")
	}
	if !validApproval(message.Approval) {
		return errors.New(`approval must be "allow" or "deny"`)
	}
	allowed := map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}
	total := 0
	for _, image := range message.Images {
		if !allowed[image.MIMEType] {
			return fmt.Errorf("unsupported image type %q", image.MIMEType)
		}
		decoded, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			return errors.New("invalid image data")
		}
		total += len(decoded)
		if total > 8<<20 {
			return errors.New("images exceed 8 MiB total")
		}
	}
	return nil
}

func validateBlock(request *BlockRequest) error {
	if len(request.Event) > model.MaxBlockerEventLength || len(request.Until) > 64 || request.ItemID != "" && !model.ValidID(request.ItemID) {
		return errors.New("invalid blocker request")
	}
	return nil
}

func validateClearBlocker(request *ClearBlockerRequest) error {
	if len(request.BlockerID) > 64 {
		return errors.New("invalid blocker id")
	}
	return nil
}

func validateSettings(request *SettingsRequest) error {
	if len(request.Settings) == 0 || len(request.Settings) > 8 {
		return errors.New("one to eight settings required")
	}
	for name, value := range request.Settings {
		if len(name) > 64 || len(value) > 256 {
			return errors.New("setting names and values must be short")
		}
	}
	return nil
}

func validateClose(request *CloseRequest) error {
	if !model.ValidResolution(request.Resolution) {
		return errors.New("resolution must be completed, abandoned, or superseded")
	}
	return nil
}

func decode(w http.ResponseWriter, r *http.Request, out any, limit int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("one JSON value required")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// badRequest rejects malformed input before it reaches Wi.
func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, Outcome{Error: err.Error()})
}

// reply reports a read; a failure is a missing item, or a conflict with
// current Wi state.
func reply[T any](w http.ResponseWriter, value T, err error) {
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, ErrNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, Outcome{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func outcome(w http.ResponseWriter, value Outcome) {
	status := http.StatusOK
	if value.Error != "" {
		status = http.StatusConflict
	}
	writeJSON(w, status, value)
}
