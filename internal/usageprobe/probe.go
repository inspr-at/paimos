// SPDX-License-Identifier: AGPL-3.0-only
// Package usageprobe reads a selected CLI login on its host and returns only
// normalized numbers. It never refreshes, writes, logs or returns credentials.
// Protocol provenance: steipete/CodexBar v0.73.0, 1d313fe (MIT).
package usageprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/capacity"
)

const Cooldown = 5 * time.Minute
const maximum = 64 << 10

// Target is host-local. Neither the home nor credential fields enter Result.
type Target struct{ Harness, Home, Provider string }
type Result struct {
	Readings []capacity.Reading
	Budget   *capacity.Budget
	Cause    string
}

type Probe interface {
	Capture(context.Context, Target, time.Time) Result
}
type Client struct{ HTTP *http.Client }

// These endpoints are fixed in production: no caller-supplied host can receive
// a credential. Redirects, cookies, refresh and inherited proxy settings are off.
func (c Client) get(ctx context.Context, endpoint, token string, headers map[string]string, into any) string {
	op, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(op, http.MethodGet, endpoint, nil)
	if err != nil {
		return "unavailable"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 4 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return "unavailable"
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 429:
		return "rate_limited"
	case 401, 403:
		return "authentication_failed"
	case 200:
	default:
		return "unavailable"
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maximum+1))
	if err != nil || len(raw) > maximum || json.Unmarshal(raw, into) != nil {
		return "protocol"
	}
	return ""
}

func (c Client) Capture(ctx context.Context, t Target, now time.Time) Result {
	if ctx.Err() != nil {
		return Result{Cause: "unavailable"}
	}
	switch t.Harness {
	case "grok":
		return c.grok(ctx, t.Home, now)
	case "claude":
		return c.claude(ctx, t.Home, now)
	case "codex":
		return c.codex(ctx, t.Home, now)
	case "pi":
		if t.Provider == "openrouter" {
			return c.openrouter(ctx, t.Home, now)
		}
	}
	return Result{Cause: "unsupported"}
}

func readLogin(home, name string, into any) bool {
	raw, err := agentsetup.ReadPrivateFile(filepath.Join(home, name), maximum)
	return err == nil && json.Unmarshal(raw, into) == nil
}
func usable(token string) bool {
	return len(token) >= 8 && len(token) <= 16<<10 && !strings.ContainsAny(token, "\x00\r\n \t")
}
func reading(kind, bucket string, minutes int, used *float64, reset time.Time, now time.Time) (capacity.Reading, bool) {
	if used == nil {
		return capacity.Reading{}, false
	}
	r := capacity.Reading{WindowKind: kind, Bucket: bucket, WindowMinutes: minutes, UsedPercent: *used, ResetsAt: reset, Source: "agentd", ReadAt: now}
	return r, r.Validate(now) == nil
}

func (c Client) grok(ctx context.Context, home string, now time.Time) Result {
	var login map[string]struct {
		Key     string    `json:"key"`
		Expires time.Time `json:"expires_at"`
	}
	if !readLogin(home, "auth.json", &login) {
		return Result{Cause: "authentication_failed"}
	}
	keys := []string{}
	for scope, l := range login {
		if strings.HasPrefix(scope, "https://auth.x.ai::") && usable(l.Key) {
			keys = append(keys, scope)
		}
	}
	sort.Strings(keys)
	// Never guess between multiple logged-in principals in a single file.
	if len(keys) == 0 && usable(login["https://accounts.x.ai/sign-in"].Key) {
		keys = append(keys, "https://accounts.x.ai/sign-in")
	}
	if len(keys) != 1 {
		return Result{Cause: "authentication_failed"}
	}
	l := login[keys[0]]
	if !l.Expires.IsZero() && !now.Before(l.Expires) {
		return Result{Cause: "authentication_failed"}
	}
	var body struct {
		Config *struct {
			Used    *float64 `json:"creditUsagePercent"`
			Current *struct {
				Start time.Time `json:"start"`
				End   time.Time `json:"end"`
			} `json:"currentPeriod"`
			Start time.Time `json:"billingPeriodStart"`
			End   time.Time `json:"billingPeriodEnd"`
		} `json:"config"`
	}
	if cause := c.get(ctx, "https://cli-chat-proxy.grok.com/v1/billing?format=credits", l.Key, map[string]string{"x-xai-token-auth": "xai-grok-cli"}, &body); cause != "" {
		return Result{Cause: cause}
	}
	if body.Config == nil {
		return Result{Cause: "protocol"}
	}
	v := body.Config
	start, end := v.Start, v.End
	if v.Current != nil && !v.Current.End.IsZero() {
		start, end = v.Current.Start, v.Current.End
	}
	if start.IsZero() || end.IsZero() || start.After(now) || !end.After(start) {
		return Result{Cause: "protocol"}
	}
	r, ok := reading("monthly", "", int(end.Sub(start)/time.Minute), v.Used, end, now)
	if !ok {
		return Result{Cause: "protocol"}
	}
	return Result{Readings: []capacity.Reading{r}}
}

type claudeOAuth struct {
	Token   string   `json:"accessToken"`
	Expires float64  `json:"expiresAt"`
	Scopes  []string `json:"scopes"`
}

// A file login stays ahead of Keychain only while it is usable and unexpired.
// Missing, unreadable, empty, and stale files fall through. The rejected file
// token is never sent.
func claudeSessionUsable(o *claudeOAuth, now time.Time) bool {
	if o == nil || !usable(o.Token) {
		return false
	}
	profile := false
	for _, s := range o.Scopes {
		profile = profile || s == "user:profile"
	}
	if !profile || o.Expires > 0 && !now.Before(time.UnixMilli(int64(o.Expires))) {
		return false
	}
	return true
}

func (c Client) claude(ctx context.Context, home string, now time.Time) Result {
	var root struct {
		OAuth *claudeOAuth `json:"claudeAiOauth"`
	}
	if !readLogin(home, ".credentials.json", &root) || !claudeSessionUsable(root.OAuth, now) {
		raw, err := loadClaudeKeychain(home)
		if err != nil || json.Unmarshal(raw, &root) != nil || !claudeSessionUsable(root.OAuth, now) {
			return Result{Cause: "authentication_failed"}
		}
	}
	o := root.OAuth
	var windows map[string]json.RawMessage
	if cause := c.get(ctx, "https://api.anthropic.com/api/oauth/usage", o.Token, map[string]string{"anthropic-beta": "oauth-2025-04-20"}, &windows); cause != "" {
		return Result{Cause: cause}
	}
	out := Result{}
	// Only known window names survive. No response label is sent to PAIMOS.
	for _, key := range []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_oauth_apps"} {
		raw := windows[key]
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var w struct {
			Used  *float64  `json:"utilization"`
			Reset time.Time `json:"resets_at"`
		}
		if json.Unmarshal(raw, &w) != nil {
			return Result{Cause: "protocol"}
		}
		kind, bucket, minutes := "weekly", key, 10080
		if key == "five_hour" {
			kind, bucket, minutes = "5h", "", 300
		}
		if key == "seven_day" {
			bucket = ""
		}
		r, ok := reading(kind, bucket, minutes, w.Used, w.Reset, now)
		if !ok {
			return Result{Cause: "protocol"}
		}
		out.Readings = append(out.Readings, r)
	}
	if len(out.Readings) == 0 {
		out.Cause = "protocol"
	}
	return out
}

func (c Client) codex(ctx context.Context, home string, now time.Time) Result {
	var login struct {
		Tokens *struct {
			Access  string `json:"access_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
	}
	if !readLogin(home, "auth.json", &login) || login.Tokens == nil || !usable(login.Tokens.Access) || !usable(login.Tokens.Account) || len(login.Tokens.Account) > 256 {
		return Result{Cause: "authentication_failed"}
	}
	type window struct {
		Used    *float64 `json:"used_percent"`
		Reset   int64    `json:"reset_at"`
		Seconds int      `json:"limit_window_seconds"`
	}
	var body struct {
		Limit *struct {
			Primary   *window `json:"primary_window"`
			Secondary *window `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if cause := c.get(ctx, "https://chatgpt.com/backend-api/wham/usage", login.Tokens.Access, map[string]string{"ChatGPT-Account-Id": login.Tokens.Account}, &body); cause != "" {
		return Result{Cause: cause}
	}
	if body.Limit == nil {
		return Result{Cause: "protocol"}
	}
	out := Result{}
	for _, w := range []*window{body.Limit.Primary, body.Limit.Secondary} {
		if w == nil {
			continue
		}
		if w.Seconds < 60 || w.Seconds > 527040*60 || w.Seconds%60 != 0 {
			return Result{Cause: "protocol"}
		}
		kind := "other"
		if w.Seconds == 300*60 {
			kind = "5h"
		}
		if w.Seconds == 10080*60 {
			kind = "weekly"
		}
		r, ok := reading(kind, "", w.Seconds/60, w.Used, time.Unix(w.Reset, 0), now)
		if !ok {
			return Result{Cause: "protocol"}
		}
		out.Readings = append(out.Readings, r)
	}
	if len(out.Readings) == 0 {
		out.Cause = "protocol"
	}
	return out
}

func (c Client) openrouter(ctx context.Context, home string, now time.Time) Result {
	var login struct {
		Router struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		} `json:"openrouter"`
	}
	if !readLogin(home, "auth.json", &login) || login.Router.Type != "api_key" || !usable(login.Router.Key) {
		return Result{Cause: "authentication_failed"}
	}
	var body struct {
		Data *struct {
			Usage     *float64 `json:"usage"`
			Limit     *float64 `json:"limit"`
			Remaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	if cause := c.get(ctx, "https://openrouter.ai/api/v1/key", login.Router.Key, nil, &body); cause != "" {
		return Result{Cause: cause}
	}
	if body.Data == nil || body.Data.Usage == nil {
		return Result{Cause: "protocol"}
	}
	v := body.Data
	b := &capacity.Budget{Currency: "USD", Source: "agentd", ReadAt: now, KeyUsageUSD: *v.Usage, KeyLimitUSD: v.Limit, KeyRemainingUSD: v.Remaining}
	if b.KeyLimitUSD == nil {
		b.KeyRemainingUSD = nil
	} else if *b.KeyLimitUSD == 0 {
		zero := 0.0
		b.KeyRemainingUSD = &zero
	}
	if b.Validate(now) != nil {
		return Result{Cause: "protocol"}
	}
	var credits struct {
		Data *struct {
			Total *float64 `json:"total_credits"`
			Used  *float64 `json:"total_usage"`
		} `json:"data"`
	}
	cause := c.get(ctx, "https://openrouter.ai/api/v1/credits", login.Router.Key, nil, &credits)
	if cause == "rate_limited" {
		return Result{Cause: cause}
	}
	// /credits can require a management key. Do not create one or invent balance.
	if cause == "" && credits.Data != nil && credits.Data.Total != nil && credits.Data.Used != nil {
		for _, v := range []float64{*credits.Data.Total, *credits.Data.Used} {
			if v < 0 || v > 1e12 || math.IsNaN(v) || math.IsInf(v, 0) {
				return Result{Cause: "protocol"}
			}
		}
		remaining := *credits.Data.Total - *credits.Data.Used
		if remaining < 0 {
			remaining = 0
		}
		b.BalanceUSD = &remaining
		if b.Validate(now) != nil {
			return Result{Cause: "protocol"}
		}
	}
	return Result{Budget: b}
}

var errLogin = errors.New("local usage login unavailable")

// loadClaudeKeychain reads the default Claude login item. Tests replace it so
// fixtures never open the operator keychain. Every other home stays refused
// inside claudeKeychain.
var loadClaudeKeychain = claudeKeychain
