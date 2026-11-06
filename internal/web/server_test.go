package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/itemnotes"
)

const itemID = "01K00000000000000000000000"

type fakeBackend struct {
	attachments map[string]itemnotes.Attachment
}

func (b *fakeBackend) CheckItem(_ context.Context, id string) error {
	if id == missingItem {
		return ErrNotFound
	}
	return nil
}

// missingItem is a valid ID no item has.
const missingItem = "01M5ZZZZZZZZZZZZZZZZZZZZZZ"

func (b *fakeBackend) Attachment(_ context.Context, _, digest string) (io.ReadCloser, itemnotes.Attachment, error) {
	attachment, ok := b.attachments[digest]
	if !ok {
		return nil, itemnotes.Attachment{}, ErrNoAttachment
	}
	return io.NopCloser(strings.NewReader("content of " + attachment.Name)), attachment, nil
}

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
		{"host rebinding", "/api/items/" + itemID + "/attachments/" + strings.Repeat("a", 64), "GET", func(r *http.Request) { r.Host = "attacker.test:1234" }},
		{"cross site read", "/api/items/" + itemID + "/attachments/" + strings.Repeat("a", 64), "GET", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"rebound stream", "/api/items/" + itemID + "/stream", "GET", func(r *http.Request) { r.Host = "attacker.test:1234" }},
		{"cross site attachment", "/api/items/" + itemID + "/attachments/" + strings.Repeat("a", 64), "GET", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
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
	r := httptest.NewRequest("GET", "https://macbook.example.ts.net:8448/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
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

// The page reads and acts through the API; wi web serves only media at
// URLs, and paths stay canonical.
func TestOnlyMediaRoutesRemain(t *testing.T) {
	s := local(t, &fakeBackend{})
	for _, path := range []string{"/api/items", "/api/events", "/api/items/" + itemID + "/chat", "/api/items/" + itemID + "/settings", "/api/agent-profiles/pi/models"} {
		if w := request(s, "GET", path, ""); w.Code != 404 && w.Code != 405 {
			t.Fatalf("GET %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/close", "/messages", "/start", "/archive"} {
		if w := request(s, "POST", "/api/items/"+itemID+path, `{}`); w.Code != 404 && w.Code != 405 {
			t.Fatalf("POST %s: %d", path, w.Code)
		}
	}
	if w := request(s, "GET", "/api/items/../../etc/passwd/document", ""); w.Code != 404 {
		t.Fatal("path escape accepted")
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

// An ID no item has is not found: Wi's loose selectors must never turn it
// into another item, say one whose description mentions the ID.
func TestMissingItemIsNotFound(t *testing.T) {
	s, _ := New(&fakeBackend{}, Options{AccessURL: "http://127.0.0.1:1234"})
	for _, test := range []struct{ method, path string }{
		{"GET", "/api/items/" + missingItem + "/document?path=a.pdf"},
		{"GET", "/api/items/" + missingItem + "/attachments/" + strings.Repeat("a", 64)},
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

func TestAttachmentsRenderOnlyAsImagesOrPlainText(t *testing.T) {
	digest := func(c byte) string { return strings.Repeat(string(c), 64) }
	b := &fakeBackend{attachments: map[string]itemnotes.Attachment{
		digest('a'): {Name: "shot.png", Type: "image/png"},
		digest('b'): {Name: "REPORT.md", Type: "text/markdown; charset=utf-8"},
		digest('c'): {Name: "page.html", Type: "text/html; charset=utf-8"},
		digest('d'): {Name: "logo.svg", Type: "image/svg+xml"},
	}}
	s := local(t, b)
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
