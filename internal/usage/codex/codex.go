// Package codex queries the documented native app-server protocol without
// creating a thread or turn. Codex owns authentication; wi never reads auth.json.
package codex

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/regb/workitem/internal/usage"
)

type Adapter struct {
	Root        string
	Environment map[string]string
	Command     []string
	Now         func() time.Time
}

func (a *Adapter) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}
func (a *Adapter) Account() usage.Account { return usage.LocalAccount("codex", a.Root) }
func (a *Adapter) Quota(ctx context.Context) usage.Quota {
	q := usage.Quota{Account: a.Account(), State: usage.Unavailable, CheckedAt: a.now(), Windows: []usage.Window{}}
	fail := func(err error) usage.Quota {
		q.Detail = "Native Codex usage query failed or timed out; no model turn was started."
		if errors.Is(err, exec.ErrNotFound) {
			q.Detail = "Codex executable unavailable. Install Codex with app-server support to query quota."
		}
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) {
			switch rpcErr.Code {
			case -32602:
				q.State = usage.Unsupported
				q.Detail = "Installed Codex does not support background quota-read parameters. Upgrade Codex."
			case -32601:
				q.State = usage.Unsupported
				q.Detail = "Installed Codex does not expose the required native usage protocol. Upgrade Codex."
			case 401, 403:
				q.State = usage.ExpiredAuth
				q.Detail = "Native Codex reports rejected authentication. Sign in with Codex."
			case 429:
				q.Detail = "Native Codex reports rate limiting. No additional query was attempted."
			}
		}
		return q
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rpc, close, err := a.start(ctx)
	if err != nil {
		return fail(err)
	}
	defer close()
	if _, err = rpc.call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "wi_usage", "title": "Wi usage", "version": "1"}}); err != nil {
		return fail(err)
	}
	if err = rpc.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return fail(err)
	}
	accountRaw, err := rpc.call(2, "account/read", map[string]bool{"refreshToken": false})
	if err != nil {
		return fail(err)
	}
	var account struct {
		RequiresOpenaiAuth *bool `json:"requiresOpenaiAuth"`
		Account            *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if json.Unmarshal(accountRaw, &account) != nil {
		return fail(fmt.Errorf("invalid account response"))
	}
	if account.Account == nil {
		q.Detail = "Native Codex has no active login. Sign in with Codex."
		return q
	}
	if account.Account.Type != "chatgpt" || (account.RequiresOpenaiAuth != nil && !*account.RequiresOpenaiAuth) {
		q.State = usage.Unsupported
		q.Detail = "The selected native Codex authentication does not expose ChatGPT subscription quota."
		return q
	}
	raw, err := rpc.call(3, "account/rateLimits/read", map[string]bool{"excludeResetCreditDetails": true, "supportsLunaReserve": false})
	if err != nil {
		return fail(err)
	}
	if err = decodeLimits(raw, &q); err != nil {
		return fail(err)
	}
	knownCredits := q.Credits != nil && (q.Credits.Remaining != nil || (q.Credits.Unlimited != nil && *q.Credits.Unlimited))
	if len(q.Windows) == 0 && !knownCredits {
		q.Detail = "Native Codex returned no known remaining quota."
		return q
	}
	q.State = usage.OK
	if q.IncludedUsageAllowed != nil && !*q.IncludedUsageAllowed {
		q.Detail = "Native Codex reports included usage is blocked. Remaining percentages alone do not grant permission to run."
	}
	now := a.now()
	q.ObservedAt = &now
	return q
}

type rpcError struct {
	Code int `json:"code"`
}

func (e *rpcError) Error() string { return "native Codex RPC error" }

type client struct {
	input  io.Writer
	output *bufio.Scanner
	frames int
}

func (c *client) send(v any) error { return json.NewEncoder(c.input).Encode(v) }
func (c *client) call(id int, method string, params any) (json.RawMessage, error) {
	if err := c.send(struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{id, method, params}); err != nil {
		return nil, err
	}
	for c.output.Scan() {
		c.frames++
		if c.frames > 1000 {
			return nil, fmt.Errorf("too many native frames")
		}
		var frame struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if json.Unmarshal(c.output.Bytes(), &frame) != nil {
			return nil, fmt.Errorf("invalid native frame")
		}
		if frame.Method != "" {
			if frame.ID != nil {
				if err := c.send(map[string]any{"id": *frame.ID, "error": map[string]any{"code": -32601, "message": "Usage inspection does not supply credentials or handle interactive requests"}}); err != nil {
					return nil, err
				}
			}
			continue
		}
		if frame.ID == nil || *frame.ID != id {
			continue
		}
		if frame.Error != nil {
			return nil, frame.Error
		}
		if len(frame.Result) == 0 || string(frame.Result) == "null" {
			return nil, fmt.Errorf("missing native result")
		}
		return frame.Result, nil
	}
	return nil, fmt.Errorf("native query closed or exceeded framing bounds")
}
func (a *Adapter) start(ctx context.Context) (*client, func(), error) {
	command := a.Command
	if len(command) == 0 {
		command = []string{"codex"}
	}
	args := append(append([]string{}, command[1:]...), "app-server", "--listen", "stdio://")
	cmd := exec.CommandContext(ctx, command[0], args...)
	// Run outside the operator's checkout so usage inspection cannot load project
	// config, start a conversation there, or execute project integrations.
	dir, err := os.MkdirTemp("", "wi-codex-usage-")
	if err != nil {
		return nil, nil, err
	}
	cmd.Dir = dir
	env := map[string]string{}
	for k, v := range a.Environment {
		env[k] = v
	}
	env["CODEX_HOME"] = a.Root
	keys := []string{}
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	input, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		os.RemoveAll(dir)
		return nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		os.RemoveAll(dir)
		return nil, nil, err
	}
	close := func() {
		input.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(250 * time.Millisecond):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		os.RemoveAll(dir)
	}
	scanner := bufio.NewScanner(io.LimitReader(output, 8<<20))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	return &client{input: input, output: scanner}, close, nil
}

type nativeWindow struct {
	Used    *float64 `json:"usedPercent"`
	Minutes *int64   `json:"windowDurationMins"`
	Reset   *int64   `json:"resetsAt"`
}
type nativeLimits struct {
	LimitID   string        `json:"limitId"`
	Primary   *nativeWindow `json:"primary"`
	Secondary *nativeWindow `json:"secondary"`
	Credits   *struct {
		Balance   json.RawMessage `json:"balance"`
		Unlimited *bool           `json:"unlimited"`
	} `json:"credits"`
}

var knownBucket = regexp.MustCompile(`^codex(?:[_-][a-z0-9]+)*$`)

func decodeLimits(raw []byte, q *usage.Quota) error {
	var result struct {
		Limits  *nativeLimits           `json:"rateLimits"`
		ByID    map[string]nativeLimits `json:"rateLimitsByLimitId"`
		Allowed *bool                   `json:"ordinaryUsageAllowed"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	q.IncludedUsageAllowed = result.Allowed
	add := func(prefix string, limits nativeLimits) {
		for _, spec := range []struct {
			id string
			w  *nativeWindow
		}{{"primary", limits.Primary}, {"secondary", limits.Secondary}} {
			if spec.w == nil {
				continue
			}
			var seconds *int64
			if spec.w.Minutes != nil && *spec.w.Minutes > 0 && *spec.w.Minutes < 366*24*60 {
				v := *spec.w.Minutes * 60
				seconds = &v
			}
			if w := usage.NativeWindow(prefix+spec.id, spec.w.Used, spec.w.Reset, seconds); w != nil {
				q.Windows = append(q.Windows, *w)
			}
		}
		if limits.Credits != nil && q.Credits == nil {
			v := limits.Credits
			q.Credits = &usage.Credits{Unlimited: v.Unlimited, Unit: "provider_credits"}
			var s string
			if json.Unmarshal(v.Balance, &s) != nil {
				s = string(v.Balance)
			}
			if balance, err := strconv.ParseFloat(s, 64); err == nil && usage.Nonnegative(&balance) {
				q.Credits.Remaining = &balance
			}
			if q.Credits.Remaining == nil && q.Credits.Unlimited == nil {
				q.Credits = nil
			}
		}
	}
	keys := []string{}
	for k := range result.ByID {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		for _, k := range keys {
			// Stable opaque IDs avoid leaking unknown native labels or renaming
			// a meter when another bucket is inserted into the response.
			hash := sha256.Sum256([]byte(k))
			prefix := fmt.Sprintf("bucket_%x/", hash[:8])
			if knownBucket.MatchString(k) && len(k) <= 80 {
				prefix = k + "/"
			}
			add(prefix, result.ByID[k])
		}
	} else if result.Limits != nil {
		prefix := ""
		if id := result.Limits.LimitID; knownBucket.MatchString(id) && len(id) <= 80 {
			prefix = id + "/"
		}
		add(prefix, *result.Limits)
	}
	return nil
}
