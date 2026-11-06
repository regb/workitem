package web

import (
	"strings"
	"testing"
)

func TestTerminalConfirmationRequests(t *testing.T) {
	backend := &fakeBackend{terminalClients: 2}
	server, err := New(backend, Options{AccessURL: "http://127.0.0.1:1234"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	result := request(server, "GET", "/api/items/"+itemID+"/terminal-clients", "")
	if result.Code != 200 || strings.TrimSpace(result.Body.String()) != "2" {
		t.Fatalf("preflight: %d %s", result.Code, result.Body.String())
	}
	for _, action := range []string{"close", "shelve"} {
		result = request(server, "POST", "/api/items/"+itemID+"/"+action, `{"resolution":"completed","confirm_attached_terminal":true}`)
		// Shelve has no resolution field; unknown fields must still be refused.
		if action == "shelve" {
			if result.Code != 400 {
				t.Fatalf("unknown field accepted: %d", result.Code)
			}
			result = request(server, "POST", "/api/items/"+itemID+"/shelve", `{"confirm_attached_terminal":true}`)
		}
		if result.Code != 200 {
			t.Fatalf("%s: %d %s", action, result.Code, result.Body.String())
		}
	}
	if !backend.closeRequest.ConfirmAttachedTerminal || !backend.shelveRequest.ConfirmAttachedTerminal {
		t.Fatal("confirmation flags lost")
	}
}
