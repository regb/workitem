package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/model"
)

const itemID = "01K00000000000000000000000"

type fakeBackend struct {
	sent     Message
	shelved  int
	archived int
	current  string
	projects []string
	partial  bool
	created  int
	create   Create
}

func (b *fakeBackend) Projects(context.Context) ([]Project, error) {
	return []Project{{ID: "project-a", Name: "repo", Root: "/tmp/repo"}}, nil
}
func (b *fakeBackend) Agents(context.Context) ([]AgentProfile, error) {
	return []AgentProfile{{Name: "default", Label: "Pi · Deep"}, {Name: "claude-opus", Label: "Claude · Opus"}}, nil
}
func (b *fakeBackend) Create(_ context.Context, create Create) Outcome {
	b.created++
	b.create = create
	return Outcome{Observed: &model.Manifest{ID: itemID, Title: "New conversation", State: model.StateBacklog}}
}
func (b *fakeBackend) StartCreated(_ context.Context, id string) Outcome {
	out := Outcome{Observed: &model.Manifest{ID: id, Title: "Generated title", State: model.StateWorking}}
	if b.partial {
		out.Error = "Created ticket, but runtime startup failed"
		out.Warnings = []string{"The created ticket is retained."}
	}
	return out
}
func (b *fakeBackend) List(context.Context) (List, error) {
	return List{}, nil
}
func (b *fakeBackend) Next(_ context.Context, id string, projects []string) (string, error) {
	b.current = id
	b.projects = projects
	return itemID, nil
}
func (b *fakeBackend) Chat(context.Context, string) (Chat, error) {
	return Chat{Description: "<b>Ticket description</b>", Project: Project{ID: "project-a", Name: "demo"}, Item: model.Manifest{ID: itemID, Title: "<script>alert(1)</script>"}, History: driverprotocol.History{Messages: []driverprotocol.HistoryMessage{{ID: "m1", Role: "user", Text: "<img src=x onerror=alert(1)>"}}}}, nil
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
func (b *fakeBackend) Archive(context.Context, string) Outcome {
	b.archived++
	if b.partial {
		return Outcome{Error: "runtime did not exit; archive not applied", Warnings: []string{"driver accepted shutdown"}, Observed: &model.Manifest{ID: itemID, State: model.StateWorking}}
	}
	return Outcome{Result: "archived"}
}
func request(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+s.host+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+s.token)
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
	s, _ := New(b, "127.0.0.1:1234")
	for _, test := range []struct {
		name, path, method string
		mutate             func(*http.Request)
	}{
		{"missing auth", "/api/items", "GET", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"host rebinding", "/api/items", "GET", func(r *http.Request) { r.Host = "attacker.test:1234" }},
		{"foreign origin", "/api/items/" + itemID + "/archive", "POST", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }},
		{"missing origin", "/api/items/" + itemID + "/archive", "POST", func(r *http.Request) { r.Header.Del("Origin") }},
		{"cross site read", "/api/items", "GET", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"form post", "/api/items/" + itemID + "/archive", "POST", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }},
		{"unauth history", "/api/items/" + itemID + "/chat", "GET", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"unauth background receipts", "/api/creations", "GET", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"unauth projects", "/api/projects", "GET", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"unauth create", "/api/items", "POST", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"cross origin create", "/api/items", "POST", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }},
		{"unauth stream", "/api/items/" + itemID + "/stream", "GET", func(r *http.Request) { r.Header.Del("Authorization") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, "http://"+s.host+test.path, strings.NewReader("{}"))
			r.Header.Set("Authorization", "Bearer "+s.token)
			r.Header.Set("Origin", "http://"+s.host)
			r.Header.Set("Content-Type", "application/json")
			test.mutate(r)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
	if b.archived != 0 || b.created != 0 {
		t.Fatal("unauthorized mutation reached backend")
	}
}
func TestTrustedHTTPSAccessURL(t *testing.T) {
	b := &fakeBackend{}
	s, err := NewWithAccessURL(b, "127.0.0.1:1234", "https://macbook.example.ts.net:8448")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.URL(); !strings.HasPrefix(got, "https://macbook.example.ts.net:8448/#token=") {
		t.Fatalf("launch URL = %q", got)
	}
	r := httptest.NewRequest("POST", "https://macbook.example.ts.net:8448/api/items/"+itemID+"/archive", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("Origin", "https://macbook.example.ts.net:8448")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || b.archived != 1 {
		t.Fatalf("trusted proxy request: status=%d body=%s", w.Code, w.Body.String())
	}
	for _, bad := range []string{
		"http://macbook.example.ts.net:8448",
		"https://user@example.ts.net",
		"https://example.ts.net/path",
		"https://example.ts.net?token=bad",
	} {
		if _, err := NewWithAccessURL(b, "127.0.0.1:1234", bad); err == nil {
			t.Fatalf("accepted unsafe access URL %q", bad)
		}
	}
}

func TestNavigationDeliveryAndArchivePartialOutcome(t *testing.T) {
	b := &fakeBackend{partial: true}
	s, _ := New(b, "127.0.0.1:1234")
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
	for _, bad := range []string{
		`{"text":"","images":[],"delivery":"prompt","runtime_id":"owner"}`,
		`{"text":"image","images":[{"mime_type":"image/svg+xml","data":"PHN2Zz4="}],"delivery":"prompt","runtime_id":"owner"}`,
		`{"text":"image","images":[{"mime_type":"image/png","data":"not-base64"}],"delivery":"prompt","runtime_id":"owner"}`,
	} {
		if w := request(s, "POST", "/api/items/"+itemID+"/messages", bad); w.Code != 409 {
			t.Fatalf("bad image message accepted: %s", bad)
		}
	}
	if w := request(s, "POST", "/api/items/"+itemID+"/shelve", `{}`); w.Code != 200 || b.shelved != 1 {
		t.Fatalf("shelve status=%d calls=%d", w.Code, b.shelved)
	}
	for _, body := range []string{`{"force":true}`, `{} {}`, `{"force":false}`} {
		if w := request(s, "POST", "/api/items/"+itemID+"/archive", body); w.Code != 409 {
			t.Fatalf("bad archive body accepted: %s", body)
		}
	}
	w := request(s, "POST", "/api/items/"+itemID+"/archive", `{}`)
	if w.Code != 409 || b.archived != 1 {
		t.Fatalf("archive %d", w.Code)
	}
	var out Outcome
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Warnings) != 1 || out.Observed.State != "working" {
		t.Fatalf("partial lost: %s", w.Body.String())
	}
	if w := request(s, "GET", "/api/items/"+itemID+"/archive", ""); w.Code != 404 || b.archived != 1 {
		t.Fatal("GET archived an item")
	}
	if w := request(s, "GET", "/api/items/../../etc/passwd/chat", ""); w.Code != 404 {
		t.Fatal("path escape accepted")
	}
}
func TestCreateValidationAndRetainedOutcome(t *testing.T) {
	b := &fakeBackend{partial: true}
	s, _ := New(b, "127.0.0.1:1234")
	defer s.Close()
	if w := request(s, "GET", "/api/projects", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "project-a") {
		t.Fatalf("project catalog: %s", w.Body.String())
	}
	if w := request(s, "GET", "/api/agents", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "claude-opus") || strings.Contains(w.Body.String(), "config") {
		t.Fatalf("agent catalog: %s", w.Body.String())
	}
	for _, body := range []string{
		`{}`, `{"title":"Task","prompt":"Request"}`, `{"project_id":"project-a","title":" ","prompt":"Request"}`,
		`{"project_id":"project-a","title":"Task","prompt":" "}`, `{"project_id":"project-a","title":"Task","prompt":"Request","repo_path":"/etc"}`,
		`{"project_id":"project-a","title":"Task","prompt":"Request","force":true}`, `{} {}`,
		`{"project_id":"project-a","title":"` + strings.Repeat("x", 513) + `","prompt":"Request"}`,
		`{"project_id":"project-a","title":"Task","prompt":"` + strings.Repeat("x", (64<<10)+1) + `"}`,
	} {
		if w := request(s, "POST", "/api/items", body); w.Code != 409 || b.created != 0 {
			t.Fatalf("invalid creation accepted: %s", w.Body.String())
		}
	}
	w := request(s, "POST", "/api/items", `{"project_id":"project-a","agent":"claude-opus","prompt":" Initial request "}`)
	var out Creation
	if w.Code != 202 || b.created != 1 || b.create.Agent != "claude-opus" || b.create.Prompt != "Initial request" || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ID != itemID || out.State != "queued" {
		t.Fatalf("creation receipt lost: %s", w.Body.String())
	}
	s.Close() // Wait for the background partial outcome before inspecting it.
	jobs := s.background.snapshot()
	if len(jobs) != 1 || jobs[0].State != "problem" || jobs[0].Error == "" || jobs[0].ID != itemID {
		t.Fatalf("startup failure lost: %+v", jobs)
	}
	if w := request(s, "GET", "/api/items", ""); w.Code != 200 || b.created != 1 {
		t.Fatal("GET created an item")
	}
}

func TestNextProjectScope(t *testing.T) {
	b := &fakeBackend{}
	s, _ := New(b, "127.0.0.1:1234")
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

func TestStreamReconnectUsesReplacementSnapshots(t *testing.T) {
	s, _ := New(&fakeBackend{}, "placeholder")
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	s.host = strings.TrimPrefix(httpServer.URL, "http://")
	s.origin = httpServer.URL
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/items/"+itemID+"/stream", nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var content driverprotocol.Content
		if err := json.NewDecoder(resp.Body).Decode(&content); err != nil {
			t.Fatal(err)
		}
		if content.Text != "live text" {
			t.Fatalf("snapshot %+v", content)
		}
		cancel()
		_ = resp.Body.Close()
	}
}
func TestEmbeddedAssetsAndBodyBounds(t *testing.T) {
	s, _ := New(&fakeBackend{}, "127.0.0.1:1234")
	navigation := httptest.NewRequest("GET", "http://"+s.host+"/", nil)
	navigation.Header.Set("Sec-Fetch-Site", "cross-site")
	navigation.Header.Set("Sec-Fetch-Mode", "navigate")
	navigation.Header.Set("Sec-Fetch-Dest", "document")
	navigationResponse := httptest.NewRecorder()
	s.ServeHTTP(navigationResponse, navigation)
	if navigationResponse.Code != 200 {
		t.Fatalf("cross-site top-level navigation status=%d", navigationResponse.Code)
	}
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		w := request(s, "GET", path, "")
		if w.Code != 200 || w.Body.Len() == 0 || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("asset %s", path)
		}
		if strings.Contains(w.Body.String(), s.token) {
			t.Fatal("token embedded in asset")
		}
	}
	w := request(s, "POST", "/api/items/"+itemID+"/messages", `{"text":"`+strings.Repeat("x", 150<<10)+`","delivery":"prompt","runtime_id":"owner"}`)
	if w.Code != 409 {
		t.Fatal("oversized body accepted")
	}
}
