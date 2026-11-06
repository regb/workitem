package web

import (
	"strings"
	"testing"
)

func TestScheduleRoutes(t *testing.T) {
	b := &fakeBackend{schedules: []Schedule{{ID: itemID, RepositoryID: itemID, Cron: "0 9 * * *", Prompt: "p", AgentProfile: "claude", Enabled: true}}}
	s, _ := New(b, Options{AccessURL: "http://127.0.0.1:1234"})
	if w := request(s, "GET", "/api/schedules", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"cron":"0 9 * * *"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{
		`{"repository_id":"` + itemID + `","cron":"","prompt":"p","agent_profile":"a"}`,
		`{"repository_id":"` + itemID + `","cron":"@daily","prompt":" ","agent_profile":"a"}`,
		`{"repository_id":"` + itemID + `","cron":"@daily","prompt":"p","agent_profile":""}`,
		`{"repository_id":"not-an-id","cron":"@daily","prompt":"p","agent_profile":"a"}`,
	} {
		if w := request(s, "POST", "/api/schedules", body); w.Code != 400 {
			t.Fatalf("invalid schedule accepted: %s -> %d", body, w.Code)
		}
	}
	if w := request(s, "POST", "/api/schedules", `{"repository_id":"`+itemID+`","cron":"@daily","prompt":"p","agent_profile":"a","enabled":true}`); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if w := request(s, "POST", "/api/schedules/"+itemID+"/run", "{}"); w.Code != 200 || !strings.Contains(w.Body.String(), `"item_id":"item"`) {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	if w := request(s, "POST", "/api/schedules/"+itemID+"/delete", "{}"); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := request(s, "POST", "/api/schedules/nope/delete", "{}"); w.Code != 400 {
		t.Fatalf("bad id: %d", w.Code)
	}
	if got := strings.Join(b.calls, " "); got != "put-schedule:@daily run-schedule:"+itemID+" delete-schedule:"+itemID {
		t.Fatalf("calls: %s", got)
	}
}
