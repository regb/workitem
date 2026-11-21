// Package antigravity reads the subscription quota of the Google account the
// Antigravity CLI (agy) is signed in with. Unlike the other providers, agy has
// no zero-turn quota command, so wi reads agy's saved access token read-only
// and asks Google's Cloud Code quota endpoint directly. wi never refreshes,
// writes, exports or logs that token; renewing it stays with agy. The token
// lasts an hour, so when it has expired wi runs `agy models`, which renews it
// without starting a conversation, and reads it again.
package antigravity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/regb/workitem/internal/usage"
)

const (
	// TokenFile is where agy keeps its Google sign-in when it uses its file
	// store, relative to its data directory (~/.gemini/antigravity-cli).
	TokenFile = "antigravity-oauth-token"
	// Consumer sign-ins are metered on the daily host and business sign-ins
	// on prod. The other host answers too but reports an untouched quota, so
	// wi never falls back to it: a wrong host would report full quota.
	dailyEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	prodEndpoint  = "https://cloudcode-pa.googleapis.com"
	summaryPath   = "/v1internal:retrieveUserQuotaSummary"
	// The quota service hides model groups from clients it considers too old:
	// without a recent Antigravity hub version in the User-Agent it omits the
	// Gemini groups and answers only the Claude and GPT one. wi names itself
	// first; 2.24.0 is the hub version observed to return every group.
	hubVersion = "2.24.0"
	maxBody    = 1 << 20
)

// Adapter reads quota for the agy data directory Root.
type Adapter struct {
	Root string
	// Endpoints overrides the quota host per sign-in method, for tests.
	Endpoints map[string]string
	Client    *http.Client
	Now       func() time.Time
	// Command runs agy; with it, an expired token is renewed through
	// `agy models`. Environment is the environment agy runs with.
	Command     []string
	Environment map[string]string
}

// renewTimeout bounds `agy models`, which takes about a second, within the
// usage cache's limit on one check.
const renewTimeout = 8 * time.Second

func (a *Adapter) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func (a *Adapter) Account() usage.Account { return usage.LocalAccount("antigravity", a.Root) }

// savedToken is agy's file-store sign-in. Only the access token, its expiry
// and the sign-in method are used; the refresh and ID tokens are ignored.
type savedToken struct {
	Token struct {
		AccessToken string `json:"access_token"`
		Expiry      string `json:"expiry"`
	} `json:"token"`
	AuthMethod string `json:"auth_method"`
}

var authMethodPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func (a *Adapter) endpoint(method string) string {
	if v, ok := a.Endpoints[method]; ok {
		return v
	}
	switch method {
	case "consumer":
		return dailyEndpoint
	case "business":
		return prodEndpoint
	}
	return ""
}

func (a *Adapter) client() *http.Client {
	c := &http.Client{Timeout: 10 * time.Second}
	if a.Client != nil {
		c = new(http.Client)
		*c = *a.Client
	}
	// Never follow a redirect: it could carry the bearer token elsewhere.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

func (a *Adapter) Quota(ctx context.Context) usage.Quota {
	q := usage.Quota{Account: a.Account(), State: usage.Unavailable, CheckedAt: a.now(), Windows: []usage.Window{}}
	fail := func(state usage.State, detail string) usage.Quota {
		q.State = state
		q.Detail = detail
		return q
	}
	saved, endpoint, state, detail := a.token()
	if detail == expiredDetail && len(a.Command) > 0 {
		a.renew(ctx)
		saved, endpoint, state, detail = a.token()
	}
	if detail != "" {
		return fail(state, detail)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+summaryPath, bytes.NewReader([]byte("{}")))
	if err != nil {
		return fail(usage.Unavailable, "Antigravity quota request could not be built.")
	}
	req.Header.Set("Authorization", "Bearer "+saved.Token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent(runtime.GOOS, runtime.GOARCH))
	requestedAt := a.now()
	resp, err := a.client().Do(req)
	if err != nil {
		// Transport errors can include the URL but never the token; still
		// report a fixed message.
		return fail(usage.Unavailable, "Antigravity quota request failed or timed out.")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fail(usage.ExpiredAuth, "Antigravity rejected the saved sign-in. Run agy to sign in again.")
	case resp.StatusCode == http.StatusTooManyRequests:
		if after := retryAfter(resp.Header.Get("Retry-After"), a.now()); after != nil {
			q.NextRefreshAt = after
		}
		return fail(usage.Unavailable, "Antigravity rate-limited the quota request. No additional request was attempted.")
	case resp.StatusCode != http.StatusOK:
		return fail(usage.Unavailable, fmt.Sprintf("Antigravity refused the quota request (HTTP %d).", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return fail(usage.Unavailable, "Antigravity quota response could not be read.")
	}
	windows, err := decodeSummary(body, requestedAt)
	if err != nil {
		return fail(usage.Unavailable, "Antigravity returned an unrecognized quota summary. Update wi.")
	}
	if len(windows) == 0 {
		return fail(usage.Unavailable, "Antigravity returned no quota buckets. Remaining quota is unknown, not exhausted.")
	}
	now := a.now()
	q.Windows = windows
	q.State = usage.OK
	q.ObservedAt = &now
	return usage.AgeObservation(q, now)
}

// expiredDetail says the saved token has expired; renewing it may help.
const expiredDetail = "Antigravity's saved access token has expired and agy did not renew it. Run agy to sign in again; wi does not refresh Antigravity tokens."

// token reads agy's saved sign-in and the quota host for it. A non-empty
// detail says why it cannot be used.
func (a *Adapter) token() (saved savedToken, endpoint string, state usage.State, detail string) {
	raw, err := usage.ReadSmallFile(filepath.Join(a.Root, TokenFile))
	if errors.Is(err, fs.ErrNotExist) {
		return saved, "", usage.Unavailable, "No Antigravity sign-in found in agy's file store. Sign in with agy; wi does not read the OS keychain."
	}
	if err != nil {
		return saved, "", usage.Unavailable, "Antigravity's saved sign-in could not be read."
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Token.AccessToken == "" {
		return savedToken{}, "", usage.Unavailable, "Antigravity's saved sign-in has an unrecognized format. Sign in again with agy or update wi."
	}
	endpoint = a.endpoint(saved.AuthMethod)
	if endpoint == "" {
		method := "unknown"
		if authMethodPattern.MatchString(saved.AuthMethod) {
			method = saved.AuthMethod
		}
		return savedToken{}, "", usage.Unsupported, fmt.Sprintf("Antigravity sign-in method %q has no known subscription quota.", method)
	}
	expiry, err := time.Parse(time.RFC3339Nano, saved.Token.Expiry)
	if err != nil || !expiry.After(a.now().Add(time.Minute)) {
		return savedToken{}, "", usage.Unavailable, expiredDetail
	}
	return saved, endpoint, usage.OK, ""
}

// renew runs `agy models` so agy renews its own sign-in. It runs outside any
// checkout, and its output is ignored.
func (a *Adapter) renew(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, renewTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "wi-agy-usage-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, a.Command[0], append(append([]string{}, a.Command[1:]...), "models")...)
	cmd.Dir = dir
	for k, v := range a.Environment {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "AGY_CLI_DISABLE_AUTO_UPDATE=1", "BROWSER=true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 250 * time.Millisecond
	_ = cmd.Run()
}

func userAgent(goos, goarch string) string {
	arch := goarch
	if arch == "amd64" {
		arch = "x64"
	}
	return fmt.Sprintf("wi-usage antigravity/hub/%s (aidev_client; os_type=%s; arch=%s)", hubVersion, goos, arch)
}

func retryAfter(value string, now time.Time) *time.Time {
	if value == "" {
		return nil
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 && seconds <= 24*60*60 {
		t := now.Add(time.Duration(seconds) * time.Second)
		return &t
	}
	if t, err := http.ParseTime(value); err == nil && t.After(now) && t.Before(now.Add(24*time.Hour)) {
		t = t.UTC()
		return &t
	}
	return nil
}

type bucket struct {
	ID          string   `json:"bucketId"`
	Window      string   `json:"window"`
	Description string   `json:"description"`
	Disabled    bool     `json:"disabled"`
	Remaining   *float64 `json:"remainingFraction"`
	ResetTime   string   `json:"resetTime"`
}

type summary struct {
	Groups []struct {
		DisplayName string   `json:"displayName"`
		Buckets     []bucket `json:"buckets"`
	} `json:"groups"`
	Buckets []bucket `json:"buckets"`
}

var windowSeconds = map[string]int64{"5h": 5 * 60 * 60, "weekly": 7 * 24 * 60 * 60}

var bucketIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)

// decodeSummary turns a retrieveUserQuotaSummary answer into windows. Groups
// name the models sharing a bucket (Gemini, Claude and GPT, ...). Buckets
// without a remaining fraction are unknown and left out, never reported as 0.
func decodeSummary(raw []byte, requestedAt time.Time) ([]usage.Window, error) {
	var s summary
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	windows := []usage.Window{}
	seen := map[string]int{}
	add := func(group string, b bucket) {
		if b.Disabled || b.Remaining == nil || math.IsNaN(*b.Remaining) || math.IsInf(*b.Remaining, 0) {
			return
		}
		remaining := math.Max(0, math.Min(1, *b.Remaining)) * 100
		w := usage.Window{ID: windowID(b.ID), RemainingPercent: &remaining}
		var duration int64
		if v, ok := windowSeconds[b.Window]; ok {
			duration = v
			w.DurationSeconds = &duration
		}
		if t, err := time.Parse(time.RFC3339Nano, b.ResetTime); err == nil && t.Year() >= 1970 && t.Year() < 10000 {
			t = t.UTC()
			// An untouched bucket reports a rolling reset one full window
			// after the request: it is when quota would reset if used now,
			// not a scheduled reset.
			untouched := *b.Remaining == 1 && b.Description == "" && duration > 0 &&
				absDuration(t.Sub(requestedAt)-time.Duration(duration)*time.Second) <= time.Minute
			if !untouched {
				w.ResetsAt = &t
			}
		}
		if label := scopeLabel(group); label != "" {
			w.Scope = &usage.WindowScope{Model: label}
		}
		seen[w.ID]++
		if seen[w.ID] > 1 {
			w.ID = fmt.Sprintf("%s_%d", w.ID, seen[w.ID])
		}
		windows = append(windows, w)
	}
	grouped := false
	for _, g := range s.Groups {
		if len(g.Buckets) > 0 {
			grouped = true
		}
		for _, b := range g.Buckets {
			add(g.DisplayName, b)
		}
	}
	if !grouped {
		for _, b := range s.Buckets {
			add("", b)
		}
	}
	return windows, nil
}

// windowID keeps known bucket IDs readable and hashes unexpected ones so a
// native label never leaks into IDs unsanitized.
func windowID(id string) string {
	if bucketIDPattern.MatchString(id) {
		return id
	}
	hash := sha256.Sum256([]byte(id))
	return fmt.Sprintf("bucket_%x", hash[:8])
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func scopeLabel(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return s
}
