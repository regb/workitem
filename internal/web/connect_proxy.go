package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"path"
	"strings"
	"time"
)

// connectPrefix is where the page calls the daemon's API, wi.core.v1, with
// the generated Connect client: the proxy forwards those calls unchanged.
const connectPrefix = "/wi.core.v1."

// maxConnectBody bounds a call's request, such as a message with images.
const maxConnectBody = 32 << 20

// connectContentTypes are the Connect protocol's: unary calls and streams,
// in JSON or binary.
var connectContentTypes = map[string]bool{"application/json": true, "application/proto": true, "application/connect+json": true, "application/connect+proto": true}

// newConnectProxy forwards calls to the daemon's socket. Which calls wi web's
// credential may make is the daemon's decision (webProcedures in
// internal/daemonapi), not the proxy's. Streams are flushed as they come.
func newConnectProxy(socket string) http.Handler {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    16,
		IdleConnTimeout: time.Minute,
	}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = "wi"
			r.Out.Host = "wi"
		},
		Transport:     transport,
		FlushInterval: -1,
	}
}

// Bounds of a call: a unary call ends after connectCallLimit; a stream after
// connectStreamLimit, when the page reconnects, and sooner when the tab
// stops reading for connectWriteLimit.
const (
	connectCallLimit   = 2 * time.Minute
	connectStreamLimit = 10 * time.Minute
	connectWriteLimit  = 5 * time.Second
)

// serveConnect admits a call as the page's other requests are admitted, then
// forwards it under the tab's identity and the user the proxy vouches for.
// The page cannot name another client, kind or user, nor present a
// credential: every Wi- header and any Authorization it sends is dropped,
// and the call carries wi web's own credential. Refusals are Connect errors, which the client reads.
func (s *Server) serveConnect(w http.ResponseWriter, r *http.Request) {
	r, refused := s.admitBrowser(r)
	if refused != nil {
		connectRefusal(w, refused.status, refused.message)
		return
	}
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Count(r.URL.Path, "/") != 2 {
		connectRefusal(w, http.StatusNotFound, "no such procedure")
		return
	}
	contentType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	contentType = strings.TrimSpace(contentType)
	if r.Method != http.MethodPost || r.Header.Get("Origin") != s.origin || !connectContentTypes[contentType] {
		connectRefusal(w, http.StatusForbidden, "same-origin Connect call required")
		return
	}
	client := ClientID(r.Context())
	if client == "" {
		client = "web"
	}
	// A stream holds a stream slot; a call, a request slot.
	stream := strings.HasPrefix(contentType, "application/connect+")
	slots, limit := s.requests, connectCallLimit
	if stream {
		slots, limit = s.streams, connectStreamLimit
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		connectRefusal(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	defer cancel()
	out := r.Clone(ctx)
	for name := range out.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "Wi-") || name == "X-Wi-Client" || (s.identityHeader != "" && http.CanonicalHeaderKey(name) == http.CanonicalHeaderKey(s.identityHeader)) {
			out.Header.Del(name)
		}
	}
	out.Header.Del("Cookie")
	token, err := s.credential()
	if err != nil {
		connectRefusal(w, http.StatusServiceUnavailable, "wi web cannot read its daemon credential")
		return
	}
	out.Header.Set("Authorization", "Bearer "+token)
	out.Header.Set("Wi-Client-Id", client)
	out.Header.Set("Wi-Client-Kind", "web")
	if user := User(r.Context()); user != "" {
		out.Header.Set("Wi-Client-Label", user)
	}
	out.Body = http.MaxBytesReader(w, r.Body, maxConnectBody)
	s.connect.ServeHTTP(&deadlineWriter{ResponseWriter: w, limit: connectWriteLimit}, out)
}

// connectRefusal answers with a Connect error, so the generated client
// reports its message rather than a bare HTTP status.
func connectRefusal(w http.ResponseWriter, status int, message string) {
	code := map[int]string{http.StatusNotFound: "unimplemented", http.StatusForbidden: "permission_denied", http.StatusUnauthorized: "unauthenticated", http.StatusBadRequest: "invalid_argument", http.StatusTooManyRequests: "resource_exhausted", http.StatusServiceUnavailable: "unavailable"}[status]
	if code == "" {
		code = "unknown"
	}
	body, _ := json.Marshal(map[string]string{"code": code, "message": message})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// deadlineWriter gives each write limit to complete, so a tab that stops
// reading a stream frees its slot instead of holding it open.
type deadlineWriter struct {
	http.ResponseWriter
	limit time.Duration
}

// extend renews the deadline before anything goes out, headers included:
// a connection reused from an earlier request may still carry its deadline.
func (d *deadlineWriter) extend() {
	_ = http.NewResponseController(d.ResponseWriter).SetWriteDeadline(time.Now().Add(d.limit))
}

func (d *deadlineWriter) WriteHeader(status int) {
	d.extend()
	d.ResponseWriter.WriteHeader(status)
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	d.extend()
	return d.ResponseWriter.Write(p)
}

func (d *deadlineWriter) Flush() {
	d.extend()
	_ = http.NewResponseController(d.ResponseWriter).Flush()
}

// Unwrap lets the proxy reach the connection's own controls.
func (d *deadlineWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }
