package web

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/testutil"
)

// proxyCredential stands for wi web's own credential.
func proxyCredential() (string, error) { return "wi_proxy_test", nil }

// The page calls the daemon's API through the proxy, under its tab's
// identity, admitted as its other requests are.
func TestConnectCallsReachTheDaemonAsTheTab(t *testing.T) {
	socket := filepath.Join(testutil.ShortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan *http.Request, 1)
	daemon := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		seen <- r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"item":{"id":"x"}}`))
	})}
	go daemon.Serve(listener)
	t.Cleanup(func() { daemon.Close() })
	s, err := New(&fakeBackend{}, Options{AccessURL: "http://127.0.0.1:1234", DaemonSocket: socket, DaemonCredential: proxyCredential})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+s.host+path, strings.NewReader(`{"itemId":"x"}`))
		r.Header.Set("Origin", s.origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Wi-Client", "tab-1")
		if mutate != nil {
			mutate(r)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := call("/wi.core.v1.ItemService/GetItem", func(r *http.Request) {
		// The page cannot claim another identity or kind.
		r.Header.Set("Wi-Client-Kind", "terminal")
		r.Header.Set("Wi-Client-Id", "someone-else")
		r.Header.Set("Wi-Control-Actor", "agent")
		// Nor present a credential of its own.
		r.Header.Set("Authorization", "Bearer wi_operator_stolen")
	})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"x"`) {
		t.Fatalf("call: %d %s", w.Code, w.Body)
	}
	got := <-seen
	if got.URL.Path != "/wi.core.v1.ItemService/GetItem" || got.Header.Get("Wi-Client-Id") != "tab-1" || got.Header.Get("Wi-Client-Kind") != "web" || got.Header.Get("Wi-Control-Actor") != "" || got.Header.Get("X-Wi-Client") != "" || got.Header.Get("Authorization") != "Bearer wi_proxy_test" {
		t.Fatalf("forwarded %s %v", got.URL.Path, got.Header)
	}
	for name, mutate := range map[string]func(*http.Request){
		"cross-site":    func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"other origin":  func(r *http.Request) { r.Header.Set("Origin", "http://attacker.test") },
		"no origin":     func(r *http.Request) { r.Header.Del("Origin") },
		"form post":     func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"GET":           func(r *http.Request) { r.Method = "GET" },
		"bad client id": func(r *http.Request) { r.Header.Set("X-Wi-Client", "Tab 1") },
	} {
		if w := call("/wi.core.v1.ItemService/GetItem", mutate); w.Code < 400 {
			t.Errorf("%s admitted: %d", name, w.Code)
		}
	}
	if w := call("/wi.core.v1.ItemService/../../api/items", nil); w.Code != http.StatusNotFound {
		t.Errorf("unclean path forwarded: %d", w.Code)
	}
	if w := call("/wi.core.v1.ItemService/GetItem", func(r *http.Request) { r.Header.Set("Origin", "http://attacker.test") }); !strings.Contains(w.Body.String(), `"code":"permission_denied"`) {
		t.Errorf("refusal outside Connect's format: %d %s", w.Code, w.Body)
	}
	select {
	case r := <-seen:
		t.Fatalf("refused call reached the daemon: %s", r.URL.Path)
	default:
	}
}

// Behind an authenticating proxy, the user it vouches for reaches the daemon
// as the caller's label; the page cannot claim one.
func TestConnectCallsCarryTheProxysUser(t *testing.T) {
	socket := filepath.Join(testutil.ShortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan http.Header, 2)
	daemon := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	})}
	go daemon.Serve(listener)
	t.Cleanup(func() { daemon.Close() })
	s, err := New(&fakeBackend{}, Options{AccessURL: "https://wi.example.ts.net", IdentityHeader: "Tailscale-User-Login", DaemonSocket: socket, DaemonCredential: proxyCredential})
	if err != nil {
		t.Fatal(err)
	}
	call := func(user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://wi.example.ts.net/wi.core.v1.SystemService/GetInfo", strings.NewReader(`{}`))
		r.Header.Set("Origin", s.origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Wi-Client-Label", "forged@example.com")
		if user != "" {
			r.Header.Set("Tailscale-User-Login", user)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := call("alice@example.com"); w.Code != 200 {
		t.Fatalf("call: %d %s", w.Code, w.Body)
	}
	if got := <-seen; got.Get("Wi-Client-Label") != "alice@example.com" || got.Get("Tailscale-User-Login") != "" {
		t.Fatalf("forwarded %v", got)
	}
	if w := call(""); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"unauthenticated"`) {
		t.Fatalf("call without a user: %d %s", w.Code, w.Body)
	}
}

// Without wi web's credential the proxy cannot forward, and says so.
func TestConnectRequiresWebsCredential(t *testing.T) {
	if _, err := New(&fakeBackend{}, Options{AccessURL: "http://127.0.0.1:1234", DaemonSocket: "/tmp/daemon.sock"}); err == nil {
		t.Fatal("forwarding without a credential")
	}
}
