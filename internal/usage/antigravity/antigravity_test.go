package antigravity

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
	"github.com/regb/workitem/internal/usage"
)

// Recorded from the daily (consumer) host for a signed-in agy 1.2.16 account.
//
//go:embed testdata/quota-summary-consumer.json
var consumerSummary []byte

const secret = "ya29.test-access-token-must-not-leak"

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolatedTests(m.Run)) }

// requestedAt is when the recorded summary was answered: untouched buckets
// reset one full window after it.
var requestedAt = time.Date(2026, 11, 20, 23, 12, 40, 0, time.UTC)

type server struct {
	status  int
	body    []byte
	headers map[string]string
	calls   int
	auth    string
	agent   string
	path    string
}

func fixture(t *testing.T, s *server, token any) *Adapter {
	t.Helper()
	root := t.TempDir()
	if token != nil {
		raw, ok := token.([]byte)
		if !ok {
			var err error
			if raw, err = json.Marshal(token); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, TokenFile), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.auth = r.Header.Get("Authorization")
		s.agent = r.Header.Get("User-Agent")
		s.path = r.Method + " " + r.URL.Path
		body, _ := io.ReadAll(r.Body)
		if string(body) != "{}" {
			t.Errorf("request body %q", body)
		}
		for k, v := range s.headers {
			w.Header().Set(k, v)
		}
		if s.status != 0 {
			w.WriteHeader(s.status)
		}
		_, _ = w.Write(s.body)
	}))
	t.Cleanup(srv.Close)
	return &Adapter{Root: root, Endpoints: map[string]string{"consumer": srv.URL, "business": srv.URL}, Client: srv.Client(), Now: func() time.Time { return requestedAt }}
}

func signedIn(method string, expiry time.Time) map[string]any {
	return map[string]any{
		"token":       map[string]any{"access_token": secret, "token_type": "Bearer", "refresh_token": "1//refresh", "expiry": expiry.Format(time.RFC3339Nano)},
		"auth_method": method,
		"id_token":    "header.payload.signature",
	}
}

func TestRecordedConsumerSummary(t *testing.T) {
	s := &server{body: consumerSummary}
	a := fixture(t, s, signedIn("consumer", requestedAt.Add(55*time.Minute)))
	q := a.Quota(context.Background())
	if q.State != usage.OK || q.ObservedAt == nil || q.Detail != "" {
		t.Fatalf("%+v", q)
	}
	if s.calls != 1 || s.auth != "Bearer "+secret || s.path != "POST /v1internal:retrieveUserQuotaSummary" {
		t.Fatal(s.calls, s.path)
	}
	// Without a recent hub version the service drops the Gemini groups.
	if !strings.Contains(s.agent, "antigravity/hub/"+hubVersion+" ") {
		t.Fatal(s.agent)
	}
	type want struct {
		id, scope string
		remaining float64
		reset     string
		duration  int64
	}
	wants := []want{
		{"gemini-weekly", "Gemini Models", 99.965864, "2026-11-27T23:07:43Z", 604800},
		{"gemini-5h", "Gemini Models", 99.88054, "2026-11-21T04:07:43Z", 18000},
		// Untouched buckets report a rolling reset: no scheduled reset.
		{"3p-weekly", "Claude and GPT models", 100, "", 604800},
		{"3p-5h", "Claude and GPT models", 100, "", 18000},
		{"gemini-argon-wtu-weekly", "Gemini 4 Argon", 100, "", 604800},
	}
	if len(q.Windows) != len(wants) {
		t.Fatalf("%+v", q.Windows)
	}
	for i, w := range wants {
		got := q.Windows[i]
		if got.ID != w.id || got.Scope == nil || got.Scope.Model != w.scope || got.RemainingPercent == nil || abs(*got.RemainingPercent-w.remaining) > 1e-9 || got.DurationSeconds == nil || *got.DurationSeconds != w.duration {
			t.Fatalf("window %d: %+v", i, got)
		}
		if w.reset == "" {
			if got.ResetsAt != nil {
				t.Fatalf("window %d: rolling reset kept: %v", i, got.ResetsAt)
			}
		} else if got.ResetsAt == nil || got.ResetsAt.Format(time.RFC3339) != w.reset {
			t.Fatalf("window %d: reset %v", i, got.ResetsAt)
		}
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestUnknownAndExhaustedBuckets(t *testing.T) {
	body := `{"groups":[{"displayName":"Claude and GPT models","buckets":[
		{"bucketId":"3p-5h","window":"5h","remainingFraction":0,"resetTime":"2026-11-21T01:00:00Z"},
		{"bucketId":"3p-weekly","window":"weekly","resetTime":"2026-11-27T23:00:00Z"},
		{"bucketId":"disabled","window":"weekly","remainingFraction":0.5,"disabled":true},
		{"bucketId":"Odd Bucket\u001b","window":"monthly","remainingFraction":1.7,"resetTime":"2026-12-20T00:00:00Z"},
		{"bucketId":"3p-5h","window":"5h","remainingFraction":-1}
	]}]}`
	a := fixture(t, &server{body: []byte(body)}, signedIn("consumer", requestedAt.Add(time.Hour)))
	q := a.Quota(context.Background())
	if q.State != usage.OK || len(q.Windows) != 3 {
		t.Fatalf("%+v", q)
	}
	exhausted, odd, clamped := q.Windows[0], q.Windows[1], q.Windows[2]
	if exhausted.ID != "3p-5h" || *exhausted.RemainingPercent != 0 || exhausted.ResetsAt == nil {
		t.Fatalf("explicit zero must stay an observed exhaustion: %+v", exhausted)
	}
	if !strings.HasPrefix(odd.ID, "bucket_") || *odd.RemainingPercent != 100 || odd.DurationSeconds != nil || odd.ResetsAt == nil {
		t.Fatalf("%+v", odd)
	}
	if clamped.ID != "3p-5h_2" || *clamped.RemainingPercent != 0 {
		t.Fatalf("%+v", clamped)
	}
}

func TestUngroupedBuckets(t *testing.T) {
	body := `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.25,"description":"used","resetTime":"2026-11-21T02:00:00.5Z"}]}`
	q := fixture(t, &server{body: []byte(body)}, signedIn("business", requestedAt.Add(time.Hour))).Quota(context.Background())
	if q.State != usage.OK || len(q.Windows) != 1 || q.Windows[0].Scope != nil || *q.Windows[0].RemainingPercent != 25 || q.Windows[0].ResetsAt == nil {
		t.Fatalf("%+v", q)
	}
}

func TestFailuresAreUnknownAndNeverLeakTheToken(t *testing.T) {
	valid := signedIn("consumer", requestedAt.Add(time.Hour))
	for _, tc := range []struct {
		name   string
		token  any
		server server
		state  usage.State
		calls  int
		detail string
	}{
		{"not signed in", nil, server{body: consumerSummary}, usage.Unavailable, 0, "No Antigravity sign-in"},
		{"malformed token", []byte("{" + secret), server{body: consumerSummary}, usage.Unavailable, 0, "unrecognized format"},
		{"empty token", map[string]any{"token": map[string]any{}, "auth_method": "consumer"}, server{body: consumerSummary}, usage.Unavailable, 0, "unrecognized format"},
		{"expired token", signedIn("consumer", requestedAt.Add(30*time.Second)), server{body: consumerSummary}, usage.Unavailable, 0, "expired"},
		{"missing expiry", map[string]any{"token": map[string]any{"access_token": secret}, "auth_method": "consumer"}, server{body: consumerSummary}, usage.Unavailable, 0, "expired"},
		{"unknown method", signedIn("api-key", requestedAt.Add(time.Hour)), server{body: consumerSummary}, usage.Unsupported, 0, `"api-key"`},
		{"unsafe method label", signedIn("\x1b[31m"+secret, requestedAt.Add(time.Hour)), server{body: consumerSummary}, usage.Unsupported, 0, `"unknown"`},
		{"rejected", valid, server{status: 401, body: []byte(`{"error":{"message":"` + secret + `"}}`)}, usage.ExpiredAuth, 1, "sign in again"},
		{"forbidden", valid, server{status: 403, body: []byte(secret)}, usage.Unavailable, 1, "HTTP 403"},
		{"redirect", valid, server{status: 302, headers: map[string]string{"Location": "https://example.invalid/"}}, usage.Unavailable, 1, "HTTP 302"},
		{"garbage", valid, server{body: []byte("<html>" + secret)}, usage.Unavailable, 1, "unrecognized quota summary"},
		{"no buckets", valid, server{body: []byte(`{"groups":[{"displayName":"Gemini","buckets":[{"bucketId":"gemini-5h","window":"5h"}]}]}`)}, usage.Unavailable, 1, "unknown, not exhausted"},
		{"oversized", valid, server{body: []byte(`{"buckets":[],"description":"` + strings.Repeat("x", maxBody) + `"}`)}, usage.Unavailable, 1, "could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.server
			a := fixture(t, &s, tc.token)
			q := a.Quota(context.Background())
			if q.State != tc.state || len(q.Windows) != 0 || q.ObservedAt != nil || s.calls != tc.calls || !strings.Contains(q.Detail, tc.detail) {
				t.Fatalf("%+v calls=%d", q, s.calls)
			}
			if strings.Contains(q.Detail, secret) {
				t.Fatalf("detail leaks native content: %q", q.Detail)
			}
			encoded, _ := json.Marshal(usage.NewSnapshot([]usage.Quota{q}))
			if strings.Contains(string(encoded), secret) {
				t.Fatal("snapshot leaks the token")
			}
		})
	}
}

func TestRateLimitHonorsRetryAfter(t *testing.T) {
	s := &server{status: 429, headers: map[string]string{"Retry-After": "900"}}
	q := fixture(t, s, signedIn("consumer", requestedAt.Add(time.Hour))).Quota(context.Background())
	if q.State != usage.Unavailable || q.NextRefreshAt == nil || !q.NextRefreshAt.Equal(requestedAt.Add(15*time.Minute)) {
		t.Fatalf("%+v", q)
	}
}

func TestTokenFileSymlinkIsNotFollowed(t *testing.T) {
	s := &server{body: consumerSummary}
	a := fixture(t, s, nil)
	elsewhere := filepath.Join(t.TempDir(), "token")
	raw, _ := json.Marshal(signedIn("consumer", requestedAt.Add(time.Hour)))
	if err := os.WriteFile(elsewhere, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(a.Root, TokenFile)); err != nil {
		t.Fatal(err)
	}
	if q := a.Quota(context.Background()); q.State != usage.Unavailable || s.calls != 0 {
		t.Fatalf("%+v", q)
	}
}

func TestTokenFileIsNeverWritten(t *testing.T) {
	s := &server{body: consumerSummary}
	a := fixture(t, s, signedIn("consumer", requestedAt.Add(time.Hour)))
	path := filepath.Join(a.Root, TokenFile)
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if q := a.Quota(context.Background()); q.State != usage.OK {
		t.Fatalf("%+v", q)
	}
	after, _ := os.ReadFile(path)
	info2, _ := os.Stat(path)
	if string(before) != string(after) || !info.ModTime().Equal(info2.ModTime()) {
		t.Fatal("token file changed")
	}
	entries, _ := os.ReadDir(a.Root)
	if len(entries) != 1 {
		t.Fatal("files created in agy's data directory", entries)
	}
}

func TestDefaultEndpointsFollowTheMeteredHost(t *testing.T) {
	a := &Adapter{}
	if a.endpoint("consumer") != dailyEndpoint || a.endpoint("business") != prodEndpoint || a.endpoint("") != "" {
		t.Fatal("endpoints")
	}
	if got := userAgent("linux", "amd64"); got != "wi-usage antigravity/hub/2.24.0 (aidev_client; os_type=linux; arch=x64)" {
		t.Fatal(got)
	}
	if a.Account().Provider != "antigravity" {
		t.Fatal(a.Account())
	}
}

func TestRetryAfterBounds(t *testing.T) {
	now := requestedAt
	for _, v := range []string{"", "0", "-5", "abc", "999999"} {
		if retryAfter(v, now) != nil {
			t.Fatal(v)
		}
	}
	if at := retryAfter(now.Add(10*time.Minute).Format(http.TimeFormat), now); at == nil || !at.Equal(now.Add(10*time.Minute)) {
		t.Fatal(at)
	}
}

func TestExpiredTokenIsRenewedByAgy(t *testing.T) {
	s := server{body: consumerSummary}
	a := fixture(t, &s, signedIn("consumer", requestedAt.Add(-time.Hour)))
	fresh, err := json.Marshal(signedIn("consumer", requestedAt.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fresh.json"), fresh, 0600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	// The fake agy renews the token as agy models does.
	agy := filepath.Join(dir, "agy")
	script := "#!/bin/sh\necho \"$@\" >> \"$AGY_CALLS\"\ncp \"$FRESH\" \"$TOKEN\"\n"
	if err := os.WriteFile(agy, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.Command = []string{agy}
	a.Environment = map[string]string{"AGY_CALLS": calls, "FRESH": filepath.Join(dir, "fresh.json"), "TOKEN": filepath.Join(a.Root, TokenFile)}
	if q := a.Quota(context.Background()); q.State != usage.OK || len(q.Windows) == 0 || s.calls != 1 {
		t.Fatalf("%+v calls=%d", q, s.calls)
	}
	if got, _ := os.ReadFile(calls); string(got) != "models\n" {
		t.Fatalf("agy calls %q", got)
	}

	// A valid token is used without running agy; a renewal that fails leaves
	// the token expired.
	if q := a.Quota(context.Background()); q.State != usage.OK {
		t.Fatalf("%+v", q)
	}
	if got, _ := os.ReadFile(calls); string(got) != "models\n" {
		t.Fatalf("agy ran with a valid token: %q", got)
	}
	expired, _ := json.Marshal(signedIn("consumer", requestedAt))
	if err := os.WriteFile(filepath.Join(a.Root, TokenFile), expired, 0600); err != nil {
		t.Fatal(err)
	}
	a.Command = []string{"false"}
	if q := a.Quota(context.Background()); q.State != usage.Unavailable || !strings.Contains(q.Detail, "agy did not renew it") || s.calls != 2 {
		t.Fatalf("%+v calls=%d", q, s.calls)
	}
}
