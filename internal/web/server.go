// Package web is a local access mechanism. It does not own Wi policy or state.
package web

import (
	"bytes"
	"context"
	"embed"
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

	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
)

// dist/app is the React client built from web/ (see scripts/install-local).
// dist/placeholder.html is served when the client was not built.
//
//go:embed all:dist
var dist embed.FS

// ErrNotFound reports that no work item has an ID the page sent.
var ErrNotFound = errors.New("no work item has this ID; it may have been deleted")

// ErrNoAttachment reports that an item's log records no attachment with a
// digest the page sent.
var ErrNoAttachment = errors.New("no attachment has this digest")

// Backend is the Wi composition the page drives. It owns all policy.
type Backend interface {
	ReviewBackend
	// CheckItem reports ErrNotFound unless an item has exactly this ID. Wi
	// resolves selectors loosely (prefixes, slugs, titles, descriptions); a
	// page names items by ID only, and a missing one must not resolve to
	// another item, say one whose description mentions it.
	CheckItem(ctx context.Context, id string) error
	// Attachment opens content attached to the item, by the digest a note
	// recorded.
	Attachment(ctx context.Context, id, digest string) (io.ReadCloser, itemnotes.Attachment, error)
}

type clientKey struct{}

type userKey struct{}

// ClientID returns the browser tab identity sent with the request. Control
// leases are held per tab so two open tabs do not share control.
func ClientID(ctx context.Context) string {
	value, _ := ctx.Value(clientKey{}).(string)
	return value
}

// User returns who the authenticating proxy says sent the request, or "" when
// no identity header is configured.
func User(ctx context.Context) string {
	value, _ := ctx.Value(userKey{}).(string)
	return value
}

// Options configure access. Wi does not authenticate: whoever can reach the
// listener is the operator, so it must be reachable only by the operator or
// by a proxy that authenticates them. Host, Origin and fetch-site checks
// still keep other websites from acting through the operator's browser.
type Options struct {
	// AccessURL is the browser-facing origin: the loopback listener, or the
	// address a reverse proxy serves, such as a tailnet or tunnel hostname.
	AccessURL string
	// IdentityHeader names the header in which the proxy passes the
	// authenticated user, such as Tailscale-User-Login. Requests without it
	// are refused, so it is trustworthy only when nothing but the proxy can
	// reach the listener.
	IdentityHeader string
	// DaemonSocket is the daemon's API socket. The page calls the API,
	// wi.core.v1, through it with the generated Connect client.
	DaemonSocket string
	// DaemonCredential is wi web's own credential, which every forwarded
	// call presents: the daemon holds it to the web exposure table and
	// believes the user it names.
	DaemonCredential func() (string, error)
}

type Server struct {
	// connect forwards the page's API calls to the daemon, when set.
	connect        http.Handler
	credential     func() (string, error)
	backend        Backend
	host           string
	origin         string
	identityHeader string
	streams        chan struct{}
	requests       chan struct{}
	mux            *http.ServeMux
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
	if opts.IdentityHeader != "" && !validHeaderName(opts.IdentityHeader) {
		return nil, fmt.Errorf("invalid identity header name %q", opts.IdentityHeader)
	}
	s := &Server{backend: backend, host: parsed.Host, origin: parsed.Scheme + "://" + parsed.Host, identityHeader: opts.IdentityHeader, streams: make(chan struct{}, 16), requests: make(chan struct{}, 16)}
	if opts.DaemonSocket != "" {
		if opts.DaemonCredential == nil {
			return nil, fmt.Errorf("forwarding to the daemon needs wi web's credential")
		}
		s.connect, s.credential = newConnectProxy(opts.DaemonSocket), opts.DaemonCredential
	}
	s.routes()
	return s, nil
}

func (s *Server) Close() {}

// Address is where browsers open the page.
func (s *Server) Address() string { return s.origin + "/" }

func validHeaderName(name string) bool {
	return len(name) <= 64 && strings.Trim(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-") == "" && strings.Trim(name, "-") != ""
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("/", s.client())
	s.reviewRoutes(m)
	m.HandleFunc("GET /api/items/{id}/attachments/{digest}", s.item(s.attachment))
	s.mux = m
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Scripts come only from the embedded bundle. Inline styles are allowed
	// for component positioning and syntax highlighting; agent HTML is never
	// rendered, so this does not admit agent-controlled markup.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data: https:; connect-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if r.Host != s.host {
		http.Error(w, "invalid Host", http.StatusForbidden)
		return
	}
	// API calls answer refusals as Connect errors; serveConnect applies the
	// same Origin rule, stricter: a call must carry this origin.
	if s.connect != nil && strings.HasPrefix(r.URL.Path, connectPrefix) {
		s.serveConnect(w, r)
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

// refusal is why a request is not admitted, with its HTTP status.
type refusal struct {
	status  int
	message string
}

// admitBrowser applies what every request from the page must pass, for the
// adapter's routes and API calls alike: not cross-site, the signed-in user
// when a proxy authenticates, and a valid tab identity. It returns the
// request with the user and tab in its context.
func (s *Server) admitBrowser(r *http.Request) (*http.Request, *refusal) {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return nil, &refusal{http.StatusForbidden, "cross-site request refused"}
	}
	if s.identityHeader != "" {
		user := strings.TrimSpace(r.Header.Get(s.identityHeader))
		if user == "" {
			return nil, &refusal{http.StatusUnauthorized, "no authenticated user: open wi through its proxy"}
		}
		if len(user) > 128 || strings.IndexFunc(user, func(c rune) bool { return c < ' ' || c == 0x7f }) >= 0 {
			return nil, &refusal{http.StatusBadRequest, "invalid user identity"}
		}
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, user))
	}
	if client := strings.TrimSpace(r.Header.Get("X-Wi-Client")); client != "" {
		if len(client) > 64 || strings.Trim(client, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return nil, &refusal{http.StatusBadRequest, "invalid client identity"}
		}
		r = r.WithContext(context.WithValue(r.Context(), clientKey{}, client))
	}
	return r, nil
}

// admitAPI authenticates an API request and bounds it. It returns nil when it
// has already answered the request.
func (s *Server) admitAPI(w http.ResponseWriter, r *http.Request) (*http.Request, func()) {
	r, refusal := s.admitBrowser(r)
	if refusal != nil {
		http.Error(w, refusal.message, refusal.status)
		return nil, nil
	}
	if r.Method == http.MethodPost && (r.Header.Get("Origin") != s.origin || r.Header.Get("Content-Type") != "application/json") {
		http.Error(w, "same-origin JSON required", http.StatusForbidden)
		return nil, nil
	}
	select {
	case s.requests <- struct{}{}:
	default:
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return nil, nil
	}
	// Media reads answer at once; API calls have their own bounds.
	timeout := 15 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + 5*time.Second))
	return r.WithContext(ctx), func() { cancel(); <-s.requests }
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

var subagentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// attachment serves attached content. PDFs open in the browser's own viewer;
// images and plain text can render in the page. HTML and SVG still download.
func (s *Server) attachment(w http.ResponseWriter, r *http.Request, id string) {
	digest := r.PathValue("digest")
	if !attachmentDigest.MatchString(digest) {
		badRequest(w, errors.New("invalid attachment digest"))
		return
	}
	content, attachment, err := s.backend.Attachment(r.Context(), id, digest)
	if err != nil {
		reply(w, struct{}{}, err)
		return
	}
	defer content.Close()
	kind, _, _ := mime.ParseMediaType(attachment.Type)
	var body io.Reader = content
	if kind == "application/pdf" {
		data, err := io.ReadAll(io.LimitReader(content, itemnotes.MaxAttachmentBytes+1))
		if err != nil {
			reply(w, struct{}{}, err)
			return
		}
		if len(data) > itemnotes.MaxAttachmentBytes {
			badRequest(w, errors.New("attachment exceeds the size limit"))
			return
		}
		if bytes.HasPrefix(data, []byte("%PDF-")) {
			servePDF(w, r, data, digest, attachment.Name, "private, max-age=31536000, immutable")
			return
		}
		// A mislabeled file still downloads; it never gets an inline MIME type.
		body = bytes.NewReader(data)
	}
	switch {
	case kind == "image/png" || kind == "image/jpeg" || kind == "image/gif" || kind == "image/webp":
		w.Header().Set("Content-Type", kind)
	case strings.HasPrefix(kind, "text/") && kind != "text/html", kind == "application/json":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Name}))
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = io.Copy(w, body)
}

var attachmentDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// attachBodyBytes fits the largest attachment encoded in JSON.
const attachBodyBytes = itemnotes.MaxAttachmentBytes/3*4 + 64<<10

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// failure is a refused request's answer.
type failure struct {
	Error string `json:"error"`
}

// badRequest rejects malformed input before it reaches Wi.
func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, failure{err.Error()})
}

// reply reports a read; a failure is a missing item, or a conflict with
// current Wi state.
func reply[T any](w http.ResponseWriter, value T, err error) {
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNoAttachment) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, failure{err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, value)
}
