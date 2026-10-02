// SPDX-License-Identifier: AGPL-3.0-only

// Package openrouter provides bounded, prompt-free catalog and key checks.
package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const BaseURL = "https://openrouter.ai/api/v1"

var ErrUnavailable = errors.New("OpenRouter check unavailable; retry locally")
var ErrKey = errors.New("OpenRouter key was not accepted; check the key locally")
var ErrProfile = errors.New("OpenRouter local profile unavailable")
var slugRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*/[a-zA-Z0-9][a-zA-Z0-9._-]*(?::[a-zA-Z0-9][a-zA-Z0-9._-]*)?$`)
var keyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var nativeRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*$`)

func ValidModel(provider, model string) bool {
	if len(provider)+len(model)+1 > 128 || len(model) > 116 || strings.HasPrefix(strings.ToLower(model), "sk-") {
		return false
	}
	if provider == "openrouter" {
		return slugRE.MatchString(model)
	}
	return nativeRE.MatchString(model)
}
func DataNote(model string) bool {
	return strings.HasPrefix(model, "stealth/") || strings.HasSuffix(model, ":free") || model == "openrouter/free"
}

// Client never follows a redirect with a key and never returns response bodies,
// headers, transport errors or account labels in diagnostics.
type Client struct {
	HTTP *http.Client
	Base string
}

func (c Client) get(ctx context.Context, path, key string, maximum int64, into any) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	base := c.Base
	if base == "" {
		base = BaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return ErrUnavailable
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := http.Client{Timeout: 4 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrKey
	}
	if res.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maximum+1))
	if err != nil || int64(len(raw)) > maximum || json.Unmarshal(raw, into) != nil {
		return ErrUnavailable
	}
	return nil
}

type Credits struct {
	ObservedAt time.Time `json:"observed_at"`
	Usage      *float64  `json:"usage"`
	Limit      *float64  `json:"limit"`
	Remaining  *float64  `json:"remaining"`
}

func (c Credits) Valid() bool {
	if c.ObservedAt.IsZero() {
		return false
	}
	for _, n := range []*float64{c.Usage, c.Limit, c.Remaining} {
		if n != nil && (*n < 0 || math.IsNaN(*n) || math.IsInf(*n, 0)) {
			return false
		}
	}
	return true
}
func (c Client) CheckKey(ctx context.Context, key string) (*Credits, error) {
	if len(key) < 8 || len(key) > 1024 || !keyRE.MatchString(key) {
		return nil, ErrKey
	}
	var body struct {
		Data *struct {
			Usage     *float64 `json:"usage"`
			Limit     *float64 `json:"limit"`
			Remaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/key", key, 64<<10, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		return nil, ErrUnavailable
	}
	out := &Credits{ObservedAt: time.Now().UTC(), Usage: body.Data.Usage, Limit: body.Data.Limit, Remaining: body.Data.Remaining}
	// /key describes a key cap, never the account's total balance. A null cap
	// has no measurable remaining credit, even if a stray field was returned.
	if out.Limit == nil {
		out.Remaining = nil
	}
	if !out.Valid() {
		return nil, ErrUnavailable
	}
	return out, nil
}

type Catalog struct {
	Client  Client
	mu      sync.Mutex
	expires time.Time
	models  map[string]bool // value: both prompt and completion are free
}

// Lookup caches failures briefly too, so an outage cannot stampede OpenRouter.
// Unknown is advisory, including an unreachable catalog; it never blocks saving.
func (c *Catalog) Lookup(ctx context.Context, slug string) (status string, note bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !time.Now().Before(c.expires) {
		var body struct {
			Data []struct {
				ID      string `json:"id"`
				Pricing struct {
					Prompt     string `json:"prompt"`
					Completion string `json:"completion"`
				} `json:"pricing"`
			} `json:"data"`
		}
		c.models = nil
		c.expires = time.Now().Add(30 * time.Second)
		if c.Client.get(ctx, "/models", "", 8<<20, &body) == nil && body.Data != nil {
			c.models = map[string]bool{}
			for _, m := range body.Data {
				c.models[m.ID] = m.Pricing.Prompt == "0" && m.Pricing.Completion == "0"
			}
			c.expires = time.Now().Add(15 * time.Minute)
		}
	}
	free, known := c.models[slug]
	if known {
		return "known", DataNote(slug) || free
	}
	return "unknown", DataNote(slug)
}
