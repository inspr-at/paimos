// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// Fixed origins only. No caller-supplied destination or followed redirects;
// keys are header-only and never included in persisted errors or provenance.
var vendorURLs = map[string]string{
	"openai":     "https://api.openai.com/v1/models",
	"xai":        "https://api.x.ai/v1/models",
	"anthropic":  "https://api.anthropic.com/v1/models",
	"google":     "https://generativelanguage.googleapis.com/v1beta/models",
	"openrouter": "https://openrouter.ai/api/v1/models",
	"modelsdev":  "https://models.dev/api.json",
	"litellm":    "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json",
}
var publicCatalogSources = []string{"openrouter", "modelsdev", "litellm"}

const (
	discoveryDeadline  = 30 * time.Second
	maxDiscoveryPages  = 5
	maxDiscoveryModels = 10000
	maxDiscoveryBytes  = 16 << 20
)

func discoveryClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func fetchProviderCatalog(ctx context.Context, client *http.Client, source, key string, at time.Time) (ProviderCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryDeadline)
	defer cancel()
	out := ProviderCatalog{Facts: []CatalogFact{}, Models: []providerModel{}}
	endpoint, ok := vendorURLs[source]
	if !ok {
		return out, errors.New("unsupported vendor")
	}
	after := ""
	seenPages := map[string]bool{}
	seenModels := map[string]bool{}
	for page := 0; page < maxDiscoveryPages; page++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		address := endpoint
		q := url.Values{}
		switch source {
		case "anthropic":
			q.Set("limit", "1000")
			if after != "" {
				q.Set("after_id", after)
			}
		case "google":
			q.Set("pageSize", "1000")
			if after != "" {
				q.Set("pageToken", after)
			}
		}
		if len(q) > 0 {
			address += "?" + q.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return out, errors.New("model list request failed")
		}
		if source == "modelsdev" {
			req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PAIMOS-AEON/ModelCatalog)")
		}
		if key != "" {
			switch source {
			case "anthropic":
				req.Header.Set("x-api-key", key)
			case "google":
				req.Header.Set("x-goog-api-key", key)
			default:
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}
		if source == "anthropic" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		resp, err := client.Do(req)
		if err != nil {
			return out, errors.New("model list unavailable")
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return out, errors.New("model list rejected")
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBytes+1))
		resp.Body.Close()
		if err != nil || len(raw) > maxDiscoveryBytes {
			return out, errors.New("model list exceeds response limit")
		}
		c, err := ParseProviderCatalog(source, raw, at)
		if err != nil {
			return out, err
		}
		out.Facts = append(out.Facts, c.Facts...)
		out.Models = append(out.Models, c.Models...)
		out.Skipped += c.Skipped
		for _, f := range c.Facts {
			if f.Fact == "model_id" {
				seenModels[f.Provider+"/"+f.ModelID+"/"+f.Route] = true
			}
		}
		if len(seenModels)+out.Skipped > maxDiscoveryModels || len(out.Facts) > maxCatalogFacts {
			return out, errors.New("model list identifier limit")
		}
		if !c.HasMore {
			return out, nil
		}
		if source != "anthropic" && source != "google" || c.Next == "" || len(c.Next) > 1024 || seenPages[c.Next] {
			return out, errors.New("invalid model list pagination")
		}
		seenPages[c.Next] = true
		after = c.Next
	}
	return out, errors.New("model list pagination limit")
}

// Compatibility adapter for callers that need IDs only, with the same bounds.
func listVendorModels(ctx context.Context, client *http.Client, vendor, key string) ([]string, error) {
	c, err := fetchProviderCatalog(ctx, client, vendor, key, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, f := range c.Facts {
		if f.Fact == "model_id" {
			id := rawString(f.Value)
			if id != "" {
				seen[id] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// ID-only listings cannot assert any effort, including a synthetic default.
func discoveryObservations(vendor, evidence string, models []string) []Observation {
	return []Observation{}
}
