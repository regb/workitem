package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
)

const itemID = "01K00000000000000000000000"

type fakeBackend struct {
	subagent    string
	attachments map[string]itemnotes.Attachment
	noted       []string
	sent        Message
	shelved     int
	reopened    int
	reopenStart bool
	closed      int
	resolution  string
	current     string
	projects    []string
	partial     bool
	created     int
	create      Create
	backlog     []BacklogCreate
	stopped     int
}

func (b *fakeBackend) CheckItem(_ context.Context, id string) error {
	if id == missingItem {
		return ErrNotFound
	}
	return nil
}

// missingItem is a valid ID no item has.
const missingItem = "01M5ZZZZZZZZZZZZZZZZZZZZZZ"

func (b *fakeBackend) Projects(context.Context) ([]Project, error) {
	return []Project{{ID: "01M5AAAAAAAAAAAAAAAAAAAAAA", Name: "repo"}}, nil
}
func (b *fakeBackend) Agents(context.Context) ([]AgentProfile, error) {
	return []AgentProfile{{Name: "default", Label: "Pi · Deep"}, {Name: "claude-opus", Label: "Claude · Opus"}}, nil
}
func (b *fakeBackend) Create(_ context.Context, create Create) Outcome {
	b.created++
	b.create = create
	return Outcome{Observed: &model.Manifest{ID: itemID, Title: "New conversation", State: model.StateBacklog}}
}
func (b *fakeBackend) StartCreated(_ context.Context, id string, _ uint64) Outcome {
	out := Outcome{Observed: &model.Manifest{ID: id, Title: "Generated title", State: model.StateActive}}
	if b.partial {
		out.Error = "Created ticket, but runtime startup failed"
		out.Warnings = []string{"The created ticket is retained."}
	}
	return out
}
func (b *fakeBackend) List(context.Context, bool) (List, error) {
	return List{}, nil
}
func (b *fakeBackend) Search(_ context.Context, query, state string) (SearchResult, error) {
	return SearchResult{Items: []SearchHit{{ListItem: ListItem{ID: query, State: state}, Field: "title"}}, Total: 1}, nil
}
func (b *fakeBackend) Next(_ context.Context, id string, projects []string) (string, error) {
	b.current = id
	b.projects = projects
	return itemID, nil
}
func (b *fakeBackend) Chat(context.Context, string) (Chat, error) {
	return Chat{Description: "<b>Ticket description</b>", Project: Project{ID: "01M5AAAAAAAAAAAAAAAAAAAAAA", Name: "demo"}, Item: model.Manifest{ID: itemID, Title: "<script>alert(1)</script>"}, History: driverprotocol.History{Messages: []driverprotocol.HistoryMessage{{ID: "m1", Role: "user", Text: "<img src=x onerror=alert(1)>"}}}}, nil
}
func (b *fakeBackend) CreateBacklog(_ context.Context, request BacklogCreate) Outcome {
	b.backlog = append(b.backlog, request)
	return Outcome{Observed: &model.Manifest{ID: itemID, Title: request.Title, State: model.StateBacklog}}
}
func (b *fakeBackend) Changes(ctx context.Context, epoch string, _ uint64, wait time.Duration) (Changes, error) {
	// Like the daemon, a first request is answered at once with everything;
	// later nothing changes, so it waits, then reports the fallback.
	if epoch == "" {
		return Changes{Epoch: "test", Revision: 1, All: true}, nil
	}
	select {
	case <-ctx.Done():
		return Changes{}, ctx.Err()
	case <-time.After(wait):
	}
	return Changes{Epoch: "test", Revision: 1}, nil
}
func (b *fakeBackend) ContentStream(context.Context, string, func(driverprotocol.ContentEvent) error) error {
	return driverprotocol.ErrContentStreamUnsupported
}
func (b *fakeBackend) Content(context.Context, string) (driverprotocol.Content, error) {
	return driverprotocol.Content{ProtocolVersion: 1, RuntimeID: "owner", WorkItemID: itemID, Text: "live text"}, nil
}
func (b *fakeBackend) Send(_ context.Context, _ string, m Message) Outcome {
	b.sent = m
	return Outcome{Result: "submitted"}
}
func (b *fakeBackend) Shelve(context.Context, string) Outcome {
	b.shelved++
	return Outcome{Result: "shelved"}
}
func (b *fakeBackend) Reopen(_ context.Context, _ string, request ReopenRequest) Outcome {
	b.reopened++
	b.reopenStart = request.Start
	return Outcome{Result: "reopened"}
}
func (b *fakeBackend) Close(_ context.Context, _ string, request CloseRequest) Outcome {
	b.closed++
	b.resolution = request.Resolution
	if b.partial {
		return Outcome{Error: "runtime did not exit; close not applied", Warnings: []string{"driver accepted shutdown"}, Observed: &model.Manifest{ID: itemID, State: model.StateActive}}
	}
	return Outcome{Result: "closed"}
}
func (b *fakeBackend) History(context.Context, string, string, string) (driverprotocol.History, error) {
	return driverprotocol.History{}, nil
}
func (b *fakeBackend) SubagentHistory(_ context.Context, _, subagent, before string) (driverprotocol.History, error) {
	b.subagent = subagent + "|" + before
	return driverprotocol.History{Messages: []driverprotocol.HistoryMessage{{ID: "s", Role: "assistant", Text: "found"}}}, nil
}
func (b *fakeBackend) Notes(context.Context, string) (Notes, error) {
	return Notes{Notes: []itemnotes.Note{{Text: "Tried the cache"}}}, nil
}
func (b *fakeBackend) Attachment(_ context.Context, _, digest string) (io.ReadCloser, itemnotes.Attachment, error) {
	attachment, ok := b.attachments[digest]
	if !ok {
		return nil, itemnotes.Attachment{}, ErrNoAttachment
	}
	return io.NopCloser(strings.NewReader("content of " + attachment.Name)), attachment, nil
}
func (b *fakeBackend) AddNote(_ context.Context, _ string, request NoteRequest) Outcome {
	b.noted = append(b.noted, request.Text)
	return Outcome{}
}
func (b *fakeBackend) Attach(_ context.Context, _ string, request AttachRequest) Outcome {
	b.noted = append(b.noted, request.Name+":"+string(request.Content))
	return Outcome{}
}
func (b *fakeBackend) TakeControl(context.Context, string, TakeControl) Outcome { return Outcome{} }
func (b *fakeBackend) ReleaseControl(context.Context, string) Outcome           { return Outcome{} }
func (b *fakeBackend) Block(context.Context, string, BlockRequest) Outcome      { return Outcome{} }
func (b *fakeBackend) ClearBlocker(context.Context, string, ClearBlockerRequest) Outcome {
	return Outcome{}
}
func (b *fakeBackend) Files(_ context.Context, _ string, query string) ([]string, error) {
	return []string{"match-for-" + query}, nil
}
func (b *fakeBackend) ProjectFiles(_ context.Context, project, query string) ([]string, error) {
	return []string{project + ":" + query}, nil
}
func (b *fakeBackend) Stop(context.Context, string, TakeControl) Outcome {
	b.stopped++
	return Outcome{Result: "stopped"}
}
func (b *fakeBackend) Start(context.Context, string, StartRequest) Outcome {
	return Outcome{Result: "started"}
}
func (b *fakeBackend) Settings(context.Context, string) (app.AgentSettingsView, error) {
	return app.AgentSettingsView{Values: map[string]string{"model": "m"}}, nil
}
func (b *fakeBackend) SetSettings(_ context.Context, _ string, request SettingsRequest) Outcome {
	return Outcome{Result: request.Settings}
}
func (b *fakeBackend) MarkRead(context.Context, string, ReadRequest) Outcome { return Outcome{} }

// local builds a server whose browser origin is the loopback listener.
func local(t *testing.T, b Backend) *Server {
	t.Helper()
	s, err := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// request sends what a page sends: every request carries its tab's identity.
func request(s *Server, method, path, body string) *httptest.ResponseRecorder {
	return requestAs(s, "tab", method, path, body)
}

// requestAs sends as a tab with this identity; empty sends none.
func requestAs(s *Server, client, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+s.host+path, strings.NewReader(body))
	if client != "" {
		r.Header.Set("X-Wi-Client", client)
	}
	if method == "POST" {
		r.Header.Set("Origin", "http://"+s.host)
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestSecurityBoundary(t *testing.T) {
	b := &fakeBackend{}
	s, _ := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	for _, test := range []struct {
		name, path, method string
		mutate             func(*http.Request)
	}{
		{"host rebinding", "/api/items", "GET", func(r *http.Request) { r.Host = "attacker.test:1234" }},
		{"foreign origin", "/api/items/" + itemID + "/close", "POST", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }},
		{"missing origin", "/api/items/" + itemID + "/close", "POST", func(r *http.Request) { r.Header.Del("Origin") }},
		{"cross site read", "/api/items", "GET", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"form post", "/api/items/" + itemID + "/close", "POST", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }},
		{"cross origin create", "/api/items", "POST", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }},
		{"rebound stream", "/api/items/" + itemID + "/stream", "GET", func(r *http.Request) { r.Host = "attacker.test:1234" }},
		{"cross site history", "/api/items/" + itemID + "/chat", "GET", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, "http://"+s.host+test.path, strings.NewReader("{}"))
			r.Header.Set("Origin", "http://"+s.host)
			r.Header.Set("Content-Type", "application/json")
			test.mutate(r)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
	if b.closed != 0 || b.created != 0 {
		t.Fatal("unauthorized mutation reached backend")
	}
}
func TestTrustedHTTPSAccessURL(t *testing.T) {
	b := &fakeBackend{}
	s, err := New(b, Options{AccessURL: "https://macbook.example.ts.net:8448"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Address(); got != "https://macbook.example.ts.net:8448/" {
		t.Fatalf("address = %q", got)
	}
	r := httptest.NewRequest("POST", "https://macbook.example.ts.net:8448/api/items/"+itemID+"/close", strings.NewReader(`{"resolution":"completed"}`))
	r.Header.Set("Origin", "https://macbook.example.ts.net:8448")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || b.closed != 1 {
		t.Fatalf("trusted proxy request: status=%d body=%s", w.Code, w.Body.String())
	}
	for _, bad := range []string{
		"ftp://macbook.example.ts.net:8448",
		"https://user@example.ts.net",
		"https://example.ts.net/path",
		"https://example.ts.net?token=bad",
	} {
		if _, err := New(b, Options{AccessURL: bad}); err == nil {
			t.Fatalf("accepted unsafe access URL %q", bad)
		}
	}
}

func TestNavigationDeliveryAndClosePartialOutcome(t *testing.T) {
	b := &fakeBackend{partial: true}
	s, _ := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	if w := request(s, "GET", "/api/next?current="+itemID, ""); w.Code != 200 || b.current != itemID || b.projects != nil {
		t.Fatalf("next %d current %s", w.Code, b.current)
	}
	if w := request(s, "GET", "/api/items/"+itemID+"/chat", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `\u003cscript`) || !strings.Contains(w.Body.String(), `\u003cb\u003eTicket description`) {
		t.Fatalf("history response %s", w.Body.String())
	}
	body := `{"text":"hello","images":[{"mime_type":"image/png","data":"iVBORw0KGgo="}],"delivery":"follow_up","runtime_id":"owner"}`
	if w := request(s, "POST", "/api/items/"+itemID+"/messages", body); w.Code != 200 || b.sent.RuntimeID != "owner" || b.sent.Delivery != "follow_up" || len(b.sent.Images) != 1 {
		t.Fatalf("send %d %+v", w.Code, b.sent)
	}
	// A send needs the tab's identity: it delivers as that tab's lease.
	if refused := requestAs(s, "", "POST", "/api/items/"+itemID+"/messages", body); refused.Code != 400 || !strings.Contains(refused.Body.String(), "client identity") {
		t.Fatalf("anonymous send %d %s", refused.Code, refused.Body.String())
	}
	for _, bad := range []string{
		`{"text":"","images":[],"delivery":"prompt","runtime_id":"owner"}`,
		`{"text":"image","images":[{"mime_type":"image/svg+xml","data":"PHN2Zz4="}],"delivery":"prompt","runtime_id":"owner"}`,
		`{"text":"image","images":[{"mime_type":"image/png","data":"not-base64"}],"delivery":"prompt","runtime_id":"owner"}`,
	} {
		if w := request(s, "POST", "/api/items/"+itemID+"/messages", bad); w.Code != 400 {
			t.Fatalf("bad image message accepted: %s", bad)
		}
	}
	if w := request(s, "GET", "/api/items/"+itemID+"/files?q=srv", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "match-for-srv") {
		t.Fatalf("files %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/projects/"+itemID+"/files?q=srv", ""); w.Code != 200 || !strings.Contains(w.Body.String(), itemID+":srv") {
		t.Fatalf("project files %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/projects/not-an-id/files?q=srv", ""); w.Code != 400 {
		t.Fatalf("invalid project accepted: %d", w.Code)
	}
	if w := request(s, "GET", "/api/items/"+itemID+"/files?q="+strings.Repeat("x", 300), ""); w.Code != 400 {
		t.Fatalf("long file query accepted: %d", w.Code)
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/messages", `{"text":"also check the tests","delivery":"steer"}`); w.Code != 200 || b.sent.Delivery != "steer" {
		t.Fatalf("steer %d %+v", w.Code, b.sent)
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/messages", `{"text":"hi","delivery":"prompt","approval":"maybe"}`); w.Code != 400 {
		t.Fatalf("unknown approval answer accepted: %d", w.Code)
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/start", `{"client_id":"c-1","approval":"allow"}`); w.Code != 200 {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/start", `{"client_id":"has space"}`); w.Code != 400 {
		t.Fatalf("invalid start client id accepted: %d", w.Code)
	}
	if w := requestAs(s, "", "POST", "/api/items/"+itemID+"/stop", `{}`); w.Code != 400 || b.stopped != 0 {
		t.Fatalf("stop without a tab identity: %d", w.Code)
	}
	stop := httptest.NewRequest("POST", "http://"+s.host+"/api/items/"+itemID+"/stop", strings.NewReader(`{}`))
	stop.Header.Set("Origin", "http://"+s.host)
	stop.Header.Set("Content-Type", "application/json")
	stop.Header.Set("X-Wi-Client", "tab-1")
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, stop)
	if recorder.Code != 200 || b.stopped != 1 {
		t.Fatalf("stop %d %s", recorder.Code, recorder.Body.String())
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/shelve", `{}`); w.Code != 200 || b.shelved != 1 {
		t.Fatalf("shelve status=%d calls=%d", w.Code, b.shelved)
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/reopen", `{"start":true}`); w.Code != 200 || b.reopened != 1 || !b.reopenStart {
		t.Fatalf("reopen status=%d calls=%d start=%t", w.Code, b.reopened, b.reopenStart)
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/reopen", `{"approval":"maybe"}`); w.Code != 400 || b.reopened != 1 {
		t.Fatalf("invalid approval status=%d calls=%d", w.Code, b.reopened)
	}
	if w := request(s, "GET", "/api/search?q=cache+bug&state=closed", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"cache bug","slug":"","title":"","state":"closed"`) {
		t.Fatalf("search status=%d body=%s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"force":true}`, `{} {}`, `{}`, `{"resolution":"done"}`} {
		if w := request(s, "POST", "/api/items/"+itemID+"/close", body); w.Code != 400 {
			t.Fatalf("bad close body accepted: %s", body)
		}
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/archive", `{}`); w.Code != 404 {
		t.Fatalf("archive route still exists: %d", w.Code)
	}
	w := request(s, "POST", "/api/items/"+itemID+"/close", `{"resolution":"abandoned"}`)
	if w.Code != 409 || b.closed != 1 || b.resolution != "abandoned" {
		t.Fatalf("close %d", w.Code)
	}
	var out Outcome
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Warnings) != 1 || out.Observed.State != "active" {
		t.Fatalf("partial lost: %s", w.Body.String())
	}
	if w := request(s, "GET", "/api/items/"+itemID+"/close", ""); w.Code != 404 || b.closed != 1 {
		t.Fatal("GET closed an item")
	}
	if w := request(s, "GET", "/api/items/../../etc/passwd/chat", ""); w.Code != 404 {
		t.Fatal("path escape accepted")
	}
}
func TestCreateValidationAndRetainedOutcome(t *testing.T) {
	b := &fakeBackend{partial: true}
	s, _ := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	defer s.Close()
	if w := request(s, "GET", "/api/projects", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "01M5AAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatalf("project catalog: %s", w.Body.String())
	}
	if w := request(s, "GET", "/api/agents", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "claude-opus") || strings.Contains(w.Body.String(), "config") {
		t.Fatalf("agent catalog: %s", w.Body.String())
	}
	for _, body := range []string{
		`{}`, `{"title":"Task","prompt":"Request"}`, `{"project_id":"01M5AAAAAAAAAAAAAAAAAAAAAA","title":" ","prompt":"Request"}`,
		`{"project_id":"01M5AAAAAAAAAAAAAAAAAAAAAA","title":"Task","prompt":" "}`, `{"project_id":"01M5AAAAAAAAAAAAAAAAAAAAAA","title":"Task","prompt":"Request","repo_path":"/etc"}`,
		`{"project_id":"01M5AAAAAAAAAAAAAAAAAAAAAA","title":"Task","prompt":"Request","force":true}`, `{} {}`,
		`{"project_id":"01M5AAAAAAAAAAAAAAAAAAAAAA","title":"` + strings.Repeat("x", 513) + `","prompt":"Request"}`,
		`{"project_id":"01M5AAAAAAAAAAAAAAAAAAAAAA","title":"Task","prompt":"` + strings.Repeat("x", (64<<10)+1) + `"}`,
	} {
		if w := request(s, "POST", "/api/items", body); w.Code != 400 || b.created != 0 {
			t.Fatalf("invalid creation accepted: %s", w.Body.String())
		}
	}
	w := request(s, "POST", "/api/items", `{"project_id":"01M5AAAAAAAAAAAAAAAAAAAAAA","agent":"claude-opus","prompt":" Initial request "}`)
	var out Creation
	if w.Code != 202 || b.created != 1 || b.create.Agent != "claude-opus" || b.create.Prompt != "Initial request" || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ID != itemID || out.State != "queued" {
		t.Fatalf("creation receipt lost: %s", w.Body.String())
	}
	s.Close() // Wait for the background partial outcome before inspecting it.
	jobs := s.background.list()
	if len(jobs) != 1 || jobs[0].State != "problem" || jobs[0].Error == "" || jobs[0].ID != itemID {
		t.Fatalf("startup failure lost: %+v", jobs)
	}
	if w := request(s, "GET", "/api/items", ""); w.Code != 200 || b.created != 1 {
		t.Fatal("GET created an item")
	}
}

func TestNextProjectScope(t *testing.T) {
	b := &fakeBackend{}
	s, _ := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	for _, test := range []struct {
		query string
		want  []string
	}{
		{"", nil},
		{"?project=first&project=second", []string{"first", "second"}},
		{"?project=", []string{""}},
	} {
		w := request(s, "GET", "/api/next"+test.query, "")
		if w.Code != 200 || len(b.projects) != len(test.want) || (b.projects == nil) != (test.want == nil) {
			t.Fatalf("scope %s: status %d, projects %#v", test.query, w.Code, b.projects)
		}
		for i := range test.want {
			if b.projects[i] != test.want[i] {
				t.Fatalf("projects = %#v, want %#v", b.projects, test.want)
			}
		}
	}
}

func TestEventStreamSendsEachSnapshotOnceUntilItChanges(t *testing.T) {
	s, _ := New(&fakeBackend{}, Options{AccessURL: "http://placeholder"})
	defer s.Close()
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	s.host = strings.TrimPrefix(httpServer.URL, "http://")
	s.origin = httpServer.URL
	for connection := 0; connection < 2; connection++ {
		// Every connection starts from full snapshots, so a reconnect loses nothing.
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/events?item="+itemID, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if typ := resp.Header.Get("Content-Type"); typ != "text/event-stream" {
			t.Fatalf("content type %q", typ)
		}
		counts := map[string]int{}
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for scanner.Scan() {
			payload, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			var frame Frame
			if err := json.Unmarshal([]byte(payload), &frame); err != nil {
				t.Fatal(err)
			}
			counts[frame.Type]++
			if frame.Type == "live" && frame.Live.Text != "live text" {
				t.Fatalf("live frame %+v", frame.Live)
			}
			if frame.Type == "chat" && (frame.Chat == nil || frame.Chat.Item.ID != itemID) {
				t.Fatalf("chat frame %+v", frame)
			}
		}
		cancel()
		_ = resp.Body.Close()
		for _, kind := range []string{"list", "creations", "live", "chat"} {
			if counts[kind] != 1 {
				t.Fatalf("connection %d sent %d %s frames, want 1: %v", connection, counts[kind], kind, counts)
			}
		}
	}
	if w := request(s, "GET", "/api/events?item=not-an-id", ""); w.Code != 400 {
		t.Fatalf("invalid item status %d", w.Code)
	}
}

func TestEmbeddedAssetsAndBodyBounds(t *testing.T) {
	s, _ := New(&fakeBackend{}, Options{AccessURL: "http://127.0.0.1:1234"})
	navigation := httptest.NewRequest("GET", "http://"+s.host+"/", nil)
	navigation.Header.Set("Sec-Fetch-Site", "cross-site")
	navigation.Header.Set("Sec-Fetch-Mode", "navigate")
	navigation.Header.Set("Sec-Fetch-Dest", "document")
	navigationResponse := httptest.NewRecorder()
	s.ServeHTTP(navigationResponse, navigation)
	if navigationResponse.Code != 200 {
		t.Fatalf("cross-site top-level navigation status=%d", navigationResponse.Code)
	}
	// Client routes all receive the page, built or placeholder.
	for _, path := range []string{"/", "/items/" + itemID, "/new"} {
		w := request(s, "GET", path, "")
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("page %s: %d %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	for _, path := range []string{"/assets/missing.js", "/api/unknown"} {
		if w := request(s, "GET", path, ""); w.Code != 404 {
			t.Fatalf("%s status %d", path, w.Code)
		}
	}
	if w := request(s, "POST", "/new", "{}"); w.Code != 405 {
		t.Fatalf("POST to a page status %d", w.Code)
	}
	w := request(s, "POST", "/api/items/"+itemID+"/messages", `{"text":"`+strings.Repeat("x", 150<<10)+`","delivery":"prompt","runtime_id":"owner"}`)
	if w.Code != 400 {
		t.Fatal("oversized body accepted")
	}
}

func TestPlainHTTPTailnetAccessURL(t *testing.T) {
	s, err := New(&fakeBackend{}, Options{AccessURL: "http://macbook.example.ts.net:8448"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Address(); got != "http://macbook.example.ts.net:8448/" {
		t.Fatalf("address = %q", got)
	}
}

// Behind a proxy that names its user, a request without that name did not
// come through the proxy and is refused; one with it carries the user.
func TestIdentityHeaderIsRequiredAndCarriesTheUser(t *testing.T) {
	b := &fakeBackend{}
	s, err := New(b, Options{AccessURL: "https://wi.example.com", IdentityHeader: "Cf-Access-Authenticated-User-Email"})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	s.mux.HandleFunc("GET /api/whoami", func(w http.ResponseWriter, r *http.Request) { seen = User(r.Context()) })
	get := func(user string) int {
		r := httptest.NewRequest("GET", "https://wi.example.com/api/whoami", nil)
		if user != "" {
			r.Header.Set("Cf-Access-Authenticated-User-Email", user)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if code := get(""); code != 401 || seen != "" {
		t.Fatalf("request without a user: status=%d user=%q", code, seen)
	}
	if code := get("ada@example.com\x01"); code != 400 || seen != "" {
		t.Fatalf("control character in user: status=%d user=%q", code, seen)
	}
	if code := get("ada@example.com"); code != 200 || seen != "ada@example.com" {
		t.Fatalf("proxied request: status=%d user=%q", code, seen)
	}
	for _, bad := range []string{"Bad Header", "-", "X:Y", strings.Repeat("A", 65)} {
		if _, err := New(b, Options{AccessURL: "https://wi.example.com", IdentityHeader: bad}); err == nil {
			t.Fatalf("accepted identity header %q", bad)
		}
	}
}

func TestProxyAccessKeepsOriginChecks(t *testing.T) {
	b := &fakeBackend{}
	s, err := New(b, Options{AccessURL: "https://macbook.example.ts.net:8448"})
	if err != nil {
		t.Fatal(err)
	}
	closeItem := func(origin string) int {
		r := httptest.NewRequest("POST", "https://macbook.example.ts.net:8448/api/items/"+itemID+"/close", strings.NewReader(`{"resolution":"completed"}`))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if code := closeItem("https://macbook.example.ts.net:8448"); code != 200 || b.closed != 1 {
		t.Fatalf("proxied request: status=%d closed=%d", code, b.closed)
	}
	if code := closeItem("https://evil.example"); code != 403 || b.closed != 1 {
		t.Fatalf("cross-origin request: status=%d closed=%d", code, b.closed)
	}
}

func TestBacklogCreationSavesATitledItemWithoutStarting(t *testing.T) {
	b := &fakeBackend{}
	s, _ := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	defer s.Close()
	for _, body := range []string{`{"project_id":"p","title":""}`, `{"project_id":"","title":"Fix it"}`, `{"project_id":"p","title":"two\nlines"}`} {
		if w := request(s, "POST", "/api/backlog", body); w.Code != 400 {
			t.Fatalf("%s accepted with %d", body, w.Code)
		}
	}
	w := request(s, "POST", "/api/backlog", `{"project_id":"01M5AAAAAAAAAAAAAAAAAAAAAA","title":"  Fix the flaky test ","description":" Seen in CI. "}`)
	if w.Code != 201 || len(b.backlog) != 1 || b.backlog[0].Title != "Fix the flaky test" || b.backlog[0].Description != "Seen in CI." || b.created != 0 {
		t.Fatalf("status %d backlog %+v created %d", w.Code, b.backlog, b.created)
	}
}

// An ID no item has is not found: Wi's loose selectors must never turn it
// into another item, say one whose description mentions the ID.
func TestMissingItemIsNotFound(t *testing.T) {
	s, _ := New(&fakeBackend{}, Options{AccessURL: "http://127.0.0.1:1234"})
	for _, test := range []struct{ method, path string }{
		{"GET", "/api/items/" + missingItem + "/chat"},
		{"POST", "/api/items/" + missingItem + "/close"},
		{"GET", "/api/events?item=" + missingItem},
	} {
		r := httptest.NewRequest(test.method, "http://"+s.host+test.path, strings.NewReader("{}"))
		r.Header.Set("Origin", "http://"+s.host)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Wi-Client", "tab")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "no work item") {
			t.Errorf("%s %s: %d %s", test.method, test.path, w.Code, w.Body.String())
		}
	}
}

func TestSubagentHistoryIsReadByBoundedIdentity(t *testing.T) {
	b := &fakeBackend{}
	s := local(t, b)
	if w := request(s, "GET", "/api/items/"+itemID+"/subagents/a1b2?before=s9", ""); w.Code != 200 || b.subagent != "a1b2|s9" || !strings.Contains(w.Body.String(), "found") {
		t.Fatalf("subagent history: %d %s %q", w.Code, w.Body.String(), b.subagent)
	}
	b.subagent = ""
	for _, bad := range []string{"a.b", strings.Repeat("a", 65), "%2e%2e"} {
		if w := request(s, "GET", "/api/items/"+itemID+"/subagents/"+bad, ""); (w.Code != 400 && w.Code != 404) || b.subagent != "" {
			t.Fatalf("subagent %q: %d", bad, w.Code)
		}
	}
}

func TestAttachmentsRenderOnlyAsImagesOrPlainText(t *testing.T) {
	digest := func(c byte) string { return strings.Repeat(string(c), 64) }
	b := &fakeBackend{attachments: map[string]itemnotes.Attachment{
		digest('a'): {Name: "shot.png", Type: "image/png"},
		digest('b'): {Name: "REPORT.md", Type: "text/markdown; charset=utf-8"},
		digest('c'): {Name: "page.html", Type: "text/html; charset=utf-8"},
		digest('d'): {Name: "logo.svg", Type: "image/svg+xml"},
	}}
	s := local(t, b)
	if w := request(s, "GET", "/api/items/"+itemID+"/notes", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Tried the cache") {
		t.Fatalf("notes: %d %s", w.Code, w.Body.String())
	}
	for c, want := range map[byte]string{'a': "image/png", 'b': "text/plain; charset=utf-8", 'c': "application/octet-stream", 'd': "application/octet-stream"} {
		w := request(s, "GET", "/api/items/"+itemID+"/attachments/"+digest(c), "")
		if w.Code != 200 || w.Header().Get("Content-Type") != want || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%c: %d %q", c, w.Code, w.Header().Get("Content-Type"))
		}
		if downloads := w.Header().Get("Content-Disposition") != ""; downloads != (want == "application/octet-stream") {
			t.Fatalf("%c: disposition %q", c, w.Header().Get("Content-Disposition"))
		}
	}
	for _, bad := range []string{digest('e'), "abc", digest('A')} {
		if w := request(s, "GET", "/api/items/"+itemID+"/attachments/"+bad, ""); w.Code != 404 && w.Code != 400 {
			t.Fatalf("%s: %d", bad, w.Code)
		}
	}
}

func TestNotesAndAttachmentsAreAddedOnlyWhenValid(t *testing.T) {
	b := &fakeBackend{}
	s := local(t, b)
	if w := request(s, "POST", "/api/items/"+itemID+"/notes", `{"text":"  Chose TTL  "}`); w.Code != 200 {
		t.Fatalf("note: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/attachments", `{"name":"REPORT.md","content":"aGk="}`); w.Code != 200 {
		t.Fatalf("attach: %d %s", w.Code, w.Body.String())
	}
	for path, body := range map[string]string{
		"notes":       `{"text":"` + strings.Repeat("x", itemnotes.MaxNoteBytes+1) + `"}`,
		"attachments": `{"name":"../x","content":"aGk="}`,
	} {
		if w := request(s, "POST", "/api/items/"+itemID+"/"+path, body); w.Code != 400 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if strings.Join(b.noted, "|") != "Chose TTL|REPORT.md:hi" {
		t.Fatalf("added %q", b.noted)
	}
}
