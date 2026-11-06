package web

import (
	"strings"
	"testing"
)

func TestProfileModelsAndCreationSettings(t *testing.T) {
	b := &fakeBackend{}
	s, _ := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	if w := request(s, "GET", "/api/agent-profiles/claude/models", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"model":"test/model"`) {
		t.Fatalf("profile models: %d %s", w.Code, w.Body)
	}
	for _, name := range []string{"Bad", "default", "-x"} {
		if w := request(s, "GET", "/api/agent-profiles/"+name+"/models", ""); w.Code != 400 {
			t.Fatalf("profile %q: %d", name, w.Code)
		}
	}
	body := `{"project_id":"` + itemID + `","agent":"claude","prompt":"Do it","model":" claude-opus-5-5 ","reasoning":"high"}`
	if w := request(s, "POST", "/api/items", body); w.Code != 200 && w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if b.create.Model != "claude-opus-5-5" || b.create.Reasoning != "high" {
		t.Fatalf("create settings: %+v", b.create)
	}
	if w := request(s, "POST", "/api/items", `{"project_id":"`+itemID+`","agent":"claude","prompt":"Do it","model":"a\nb"}`); w.Code != 400 {
		t.Fatalf("invalid model accepted: %d", w.Code)
	}
	// The page's idempotency key reaches the backend; a malformed one is refused.
	if w := request(s, "POST", "/api/items", `{"project_id":"`+itemID+`","agent":"claude","prompt":"Do it","request_id":"k-1"}`); (w.Code != 200 && w.Code != 202) || b.create.RequestID != "k-1" {
		t.Fatalf("request key: %d %+v", w.Code, b.create)
	}
	if w := request(s, "POST", "/api/items", `{"project_id":"`+itemID+`","agent":"claude","prompt":"Do it","request_id":"has space"}`); w.Code != 400 {
		t.Fatalf("invalid request key accepted: %d", w.Code)
	}
}
