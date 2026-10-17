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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

//go:embed static/*
var assets embed.FS

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}
type AgentProfile struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}
type ListItem struct {
	app.WorkListItem
	ProjectID string `json:"project_id"`
}
type Section struct {
	Name  string     `json:"name"`
	Items []ListItem `json:"items"`
}
type List struct {
	app.WorkListResult
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
	IdleGuard   bool                         `json:"idle_guard"`
	History     driverprotocol.History       `json:"history"`
	Control     *ControlView                 `json:"control,omitempty"`
	Unread      bool                         `json:"unread"`
	Warnings    []string                     `json:"warnings"`
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

type BlockRequest struct {
	Kind     string            `json:"kind"`
	Duration string            `json:"duration,omitempty"`
	Until    string            `json:"until,omitempty"`
	ItemID   string            `json:"item_id,omitempty"`
	Event    string            `json:"event,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}

type ClearBlockerRequest struct {
	BlockerID string `json:"blocker_id"`
}

type ReadRequest struct {
	Unread bool `json:"unread"`
}

type CloseRequest struct {
	Resolution string `json:"resolution"`
}

// ControlBackend adds controller leases, blockers, and read markers. Backends
// without it keep the read-mostly chat behavior.
type ControlBackend interface {
	Control(ctx context.Context, id string) (ControlView, error)
	TakeControl(ctx context.Context, id string, request TakeControl) Outcome
	ReleaseControl(ctx context.Context, id string) Outcome
	Block(ctx context.Context, id string, request BlockRequest) Outcome
	ClearBlocker(ctx context.Context, id string, request ClearBlockerRequest) Outcome
	MarkRead(ctx context.Context, id string, request ReadRequest) Outcome
}

type clientKey struct{}

// ClientID returns the browser tab identity sent with the request. Control
// leases are held per tab so two open tabs do not share control.
func ClientID(ctx context.Context) string {
	value, _ := ctx.Value(clientKey{}).(string)
	return value
}

type Create struct {
	ProjectID string `json:"project_id"`
	Agent     string `json:"agent,omitempty"`
	Prompt    string `json:"prompt"`
}
type Message struct {
	Text      string                        `json:"text"`
	Images    []driverprotocol.ImageContent `json:"images,omitempty"`
	Delivery  string                        `json:"delivery"`
	RuntimeID string                        `json:"runtime_id"`
}
type Outcome struct {
	CreationUncertain bool            `json:"creation_uncertain,omitempty"`
	Result            any             `json:"result,omitempty"`
	Error             string          `json:"error,omitempty"`
	Warnings          []string        `json:"warnings,omitempty"`
	Observed          *model.Manifest `json:"observed,omitempty"`
	Control           *ControlView    `json:"control,omitempty"`
}
type Backend interface {
	Projects(context.Context) ([]Project, error)
	Agents(context.Context) ([]AgentProfile, error)
	Create(context.Context, Create) Outcome
	StartCreated(context.Context, string) Outcome
	List(context.Context) (List, error)
	Next(context.Context, string, []string) (string, error)
	Chat(context.Context, string) (Chat, error)
	Content(context.Context, string) (driverprotocol.Content, error)
	Send(context.Context, string, Message) Outcome
	Shelve(context.Context, string) Outcome
	Close(context.Context, string, CloseRequest) Outcome
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
}

func New(backend Backend, host string) (*Server, error) {
	return newServer(backend, "http://"+host)
}

// NewWithAccessURL keeps the listener local while allowing a trusted reverse
// proxy, such as a private Tailnet service, to provide the browser-facing origin.
func NewWithAccessURL(backend Backend, _ string, accessURL string) (*Server, error) {
	return newServer(backend, accessURL)
}

func newServer(backend Backend, accessURL string) (*Server, error) {
	parsed, err := url.Parse(accessURL)
	if err != nil {
		return nil, fmt.Errorf("invalid access URL: %w", err)
	}
	validScheme := parsed.Scheme == "http" || parsed.Scheme == "https"
	if !validScheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("access URL must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &Server{backend: backend, host: parsed.Host, origin: parsed.Scheme + "://" + parsed.Host, token: hex.EncodeToString(b), streams: make(chan struct{}, 16), requests: make(chan struct{}, 16), background: newBackgroundStarts(backend)}, nil
}
func (s *Server) Close() { s.background.close() }

// UseToken replaces the random launch secret with a caller-persisted one so
// the access URL survives server restarts.
func (s *Server) UseToken(token string) error {
	if b, err := hex.DecodeString(token); err != nil || len(b) != 32 {
		return fmt.Errorf("access token must be 64 hexadecimal characters")
	}
	s.token = token
	return nil
}

// DisableToken trusts the reverse proxy in front of the access URL to restrict
// who can reach the server. Host, Origin, and same-site checks still apply.
func (s *Server) DisableToken() { s.noToken = true }

func (s *Server) URL() string {
	if s.noToken {
		return s.origin + "/"
	}
	return s.origin + "/#token=" + s.token
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src data: https:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if r.Host != s.host {
		http.Error(w, "invalid Host", 403)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
		http.Error(w, "cross-origin request refused", 403)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		files := map[string]string{"/": "index.html", "/app.js": "app.js", "/style.css": "style.css"}
		file, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, _ := assets.ReadFile("static/" + file)
		typ := "text/html; charset=utf-8"
		if file == "app.js" {
			typ = "text/javascript; charset=utf-8"
		}
		if file == "style.css" {
			typ = "text/css; charset=utf-8"
		}
		w.Header().Set("Content-Type", typ)
		_, _ = w.Write(data)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		http.Error(w, "cross-site request refused", 403)
		return
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.noToken && subtle.ConstantTimeCompare([]byte(auth), []byte(s.token)) != 1 {
		http.Error(w, "open the launch URL printed by wi web", 401)
		return
	}
	if client := strings.TrimSpace(r.Header.Get("X-Wi-Client")); client != "" {
		if len(client) > 64 || strings.Trim(client, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			http.Error(w, "invalid client identity", 400)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), clientKey{}, client))
	}
	if !strings.HasSuffix(r.URL.Path, "/stream") {
		select {
		case s.requests <- struct{}{}:
			defer func() { <-s.requests }()
		default:
			http.Error(w, "too many requests", 429)
			return
		}
		// Taking control can stop one runtime and wait for another to become
		// ready on the same conversation.
		timeout := 15 * time.Second
		if r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/control") || strings.HasSuffix(r.URL.Path, "/messages")) {
			timeout = 45 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + 5*time.Second))
	}
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Method == "POST" && (r.Header.Get("Origin") != s.origin || r.Header.Get("Content-Type") != "application/json") {
		http.Error(w, "same-origin JSON required", 403)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/creations" {
		s.reply(w, s.background.snapshot(), nil)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/projects" {
		value, err := s.backend.Projects(r.Context())
		s.reply(w, value, err)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/agents" {
		value, err := s.backend.Agents(r.Context())
		s.reply(w, value, err)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/api/items" {
		var create Create
		if err := decode(w, r, &create); err != nil {
			s.reply(w, nil, err)
			return
		}
		create.Prompt = strings.TrimSpace(create.Prompt)
		create.Agent = strings.TrimSpace(create.Agent)
		if create.ProjectID == "" || len(create.ProjectID) > 128 || len(create.Agent) > 128 || create.Prompt == "" || len(create.Prompt) > 64<<10 {
			s.reply(w, nil, fmt.Errorf("project, configured agent, and prompt (up to 64 KiB) required"))
			return
		}
		// Reserve capacity before committing an item. A receipt means its prompt
		// is durable, not merely held in a browser or an in-memory queue.
		if !s.background.reserve() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			_ = json.NewEncoder(w).Encode(Outcome{Error: "Too many conversations starting. Try again shortly."})
			return
		}
		out := s.backend.Create(r.Context(), create)
		if out.Error != "" || out.Observed == nil {
			if out.Observed != nil {
				s.background.recordProblem(*out.Observed, out.Error, out.Warnings)
			}
			s.background.release()
			s.outcome(w, out)
			return
		}
		job := s.background.enqueue(*out.Observed, out.Warnings)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(job)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/items" {
		value, err := s.backend.List(r.Context())
		s.reply(w, value, err)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/next" {
		current := r.URL.Query().Get("current")
		if current != "" && !model.ValidID(current) {
			s.reply(w, nil, fmt.Errorf("invalid current item"))
			return
		}
		id, err := s.backend.Next(r.Context(), current, r.URL.Query()["project"])
		s.reply(w, map[string]string{"id": id}, err)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/items/"), "/")
	if len(parts) != 2 || !model.ValidID(parts[0]) {
		http.NotFound(w, r)
		return
	}
	id, action := parts[0], parts[1]
	switch {
	case r.Method == "GET" && action == "chat":
		job, known := s.background.lookup(id)
		if known && (job.State == "queued" || job.State == "starting") {
			s.reply(w, Chat{Item: model.Manifest{ID: id, Title: job.Title}, Startup: &job}, nil)
			return
		}
		value, err := s.backend.Chat(r.Context(), id)
		if known {
			value.Startup = &job
		}
		s.reply(w, value, err)
	case r.Method == "GET" && action == "history":
		leaf, before := r.URL.Query().Get("leaf"), r.URL.Query().Get("before")
		if leaf == "" || before == "" || len(leaf) > 4096 || len(before) > 4096 {
			s.reply(w, nil, fmt.Errorf("bounded branch and history cursor required"))
			return
		}
		reader, ok := s.backend.(interface {
			History(context.Context, string, string, string) (driverprotocol.History, error)
		})
		if !ok {
			s.reply(w, nil, fmt.Errorf("history paging unavailable"))
			return
		}
		value, err := reader.History(r.Context(), id, leaf, before)
		s.reply(w, value, err)
	case r.Method == "GET" && action == "stream":
		s.stream(w, r, id)
	case r.Method == "POST" && action == "messages":
		var message Message
		if err := decodeLimit(w, r, &message, 12<<20); err != nil {
			s.reply(w, nil, err)
			return
		}
		if err := validateMessage(message); err != nil {
			s.reply(w, nil, err)
			return
		}
		s.outcome(w, s.backend.Send(r.Context(), id, message))
	case r.Method == "POST" && action == "shelve":
		var empty struct{}
		if err := decode(w, r, &empty); err != nil {
			s.reply(w, nil, err)
			return
		}
		s.outcome(w, s.backend.Shelve(r.Context(), id))
	case r.Method == "POST" && action == "close":
		var request CloseRequest
		if err := decode(w, r, &request); err != nil {
			s.reply(w, nil, err)
			return
		}
		if !model.ValidResolution(request.Resolution) {
			s.reply(w, nil, fmt.Errorf("resolution must be completed, abandoned, or superseded"))
			return
		}
		s.outcome(w, s.backend.Close(r.Context(), id, request))
	case action == "control" || action == "release" || action == "blockers" || action == "clear-blocker" || action == "read":
		control, ok := s.backend.(ControlBackend)
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.serveControl(w, r, control, id, action)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request, control ControlBackend, id, action string) {
	if r.Method == "GET" && action == "control" {
		value, err := control.Control(r.Context(), id)
		s.reply(w, value, err)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	needsClient := action == "control" || action == "release"
	if needsClient && ClientID(r.Context()) == "" {
		s.reply(w, nil, fmt.Errorf("client identity required"))
		return
	}
	switch action {
	case "control":
		var request TakeControl
		if err := decode(w, r, &request); err != nil {
			s.reply(w, nil, err)
			return
		}
		s.outcome(w, control.TakeControl(r.Context(), id, request))
	case "release":
		var empty struct{}
		if err := decode(w, r, &empty); err != nil {
			s.reply(w, nil, err)
			return
		}
		s.outcome(w, control.ReleaseControl(r.Context(), id))
	case "blockers":
		var request BlockRequest
		if err := decode(w, r, &request); err != nil {
			s.reply(w, nil, err)
			return
		}
		if len(request.Event) > model.MaxBlockerEventLength || len(request.Duration) > 32 || len(request.Until) > 64 || request.ItemID != "" && !model.ValidID(request.ItemID) {
			s.reply(w, nil, fmt.Errorf("invalid blocker request"))
			return
		}
		s.outcome(w, control.Block(r.Context(), id, request))
	case "clear-blocker":
		var request ClearBlockerRequest
		if err := decode(w, r, &request); err != nil {
			s.reply(w, nil, err)
			return
		}
		if len(request.BlockerID) > 64 {
			s.reply(w, nil, fmt.Errorf("invalid blocker id"))
			return
		}
		s.outcome(w, control.ClearBlocker(r.Context(), id, request))
	case "read":
		var request ReadRequest
		if err := decode(w, r, &request); err != nil {
			s.reply(w, nil, err)
			return
		}
		s.outcome(w, control.MarkRead(r.Context(), id, request))
	}
}
func validateMessage(message Message) error {
	if len(message.Text) > 64<<10 || (strings.TrimSpace(message.Text) == "" && len(message.Images) == 0) || len(message.RuntimeID) > 200 || (message.Delivery != "prompt" && message.Delivery != "follow_up") {
		return fmt.Errorf("message or image and prompt/follow_up delivery required")
	}
	if len(message.Images) > 4 {
		return fmt.Errorf("at most four images are allowed")
	}
	allowed := map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}
	total := 0
	for _, image := range message.Images {
		if !allowed[image.MIMEType] {
			return fmt.Errorf("unsupported image type %q", image.MIMEType)
		}
		decoded, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			return fmt.Errorf("invalid image data")
		}
		total += len(decoded)
		if total > 8<<20 {
			return fmt.Errorf("images exceed 8 MiB total")
		}
	}
	return nil
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	return decodeLimit(w, r, out, 128<<10)
}
func decodeLimit(w http.ResponseWriter, r *http.Request, out any, limit int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("one JSON value required")
	}
	return nil
}
func (s *Server) reply(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(409)
		value = Outcome{Error: err.Error()}
	}
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) outcome(w http.ResponseWriter, value Outcome) {
	w.Header().Set("Content-Type", "application/json")
	if value.Error != "" {
		w.WriteHeader(409)
	}
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request, id string) {
	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		http.Error(w, "too many streams", 429)
		return
	}
	flush, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	for {
		value, err := s.backend.Content(r.Context(), id)
		if err != nil {
			value = driverprotocol.Content{Error: err.Error()}
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Second))
		if json.NewEncoder(w).Encode(value) != nil {
			return
		}
		flush.Flush()
		if err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}
