// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Fixed origins are the only outbound destinations. No caller supplies a URL,
// redirect, prompt or filesystem path; credentials never appear in errors.
var vendorURLs = map[string]string{
	"openai":     "https://api.openai.com/v1/models",
	"xai":        "https://api.x.ai/v1/models",
	"anthropic":  "https://api.anthropic.com/v1/models",
	"openrouter": "https://openrouter.ai/api/v1/models",
}

const (
	discoveryDeadline  = 30 * time.Second
	maxDiscoveryPages  = 5
	maxDiscoveryModels = 500
)

func discoveryClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func listVendorModels(ctx context.Context, client *http.Client, vendor, key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryDeadline)
	defer cancel()
	endpoint, ok := vendorURLs[vendor]
	if !ok {
		return nil, errors.New("unsupported vendor")
	}
	found := map[string]bool{}
	after := ""
	// Anthropic is paginated. Truncation is an error, never a disappearance signal.
	for page := 0; page < maxDiscoveryPages; page++ {
		if ctx.Err() != nil {
			return nil, errors.New("model list unavailable")
		}
		address := endpoint
		if vendor == "anthropic" {
			q := url.Values{"limit": {"1000"}}
			if after != "" {
				q.Set("after_id", after)
			}
			address += "?" + q.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, errors.New("model list request failed")
		}
		if vendor == "anthropic" {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.New("model list unavailable")
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, errors.New("model list rejected")
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
		resp.Body.Close()
		if err != nil || len(raw) > 4<<20 {
			return nil, errors.New("model list exceeds response limit")
		}
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if json.Unmarshal(raw, &body) != nil || body.Data == nil {
			return nil, errors.New("invalid model list")
		}
		for _, row := range body.Data {
			if len(row.ID) > 128 || !modelRE.MatchString(row.ID) {
				return nil, errors.New("invalid model identifier")
			}
			found[row.ID] = true
			if len(found) > maxDiscoveryModels {
				return nil, errors.New("model list identifier limit")
			}
		}
		if !body.HasMore {
			out := make([]string, 0, len(found))
			for id := range found {
				out = append(out, id)
			}
			sort.Strings(out)
			return out, nil
		}
		if vendor != "anthropic" || body.LastID == "" || body.LastID == after {
			return nil, errors.New("invalid model list pagination")
		}
		after = body.LastID
	}
	return nil, errors.New("model list pagination limit")
}

// API listings do not assert CLI success. Vendor aliases (e.g. Claude Code
// 'sonnet') aren't removed when an API returns dated identifiers instead.
func discoveryObservations(vendor, evidence string, models []string) []Observation {
	harness := map[string]string{"openai": "codex", "xai": "grok", "anthropic": "claude", "openrouter": "pi"}[vendor]
	out := []Observation{}
	for _, id := range models {
		// Lists include embeddings, images and fine-tunes; they aren't dispatch models.
		if vendor == "openai" && !strings.HasPrefix(id, "gpt-") {
			continue
		}
		if vendor == "xai" && !strings.HasPrefix(id, "grok-") {
			continue
		}
		if vendor == "anthropic" && !strings.HasPrefix(id, "claude-") {
			continue
		}
		efforts := []string{"default"}
		for _, seed := range seedModels {
			if seed.Harness == harness && seed.ID == id {
				efforts = seed.Efforts
			}
		}
		for _, effort := range efforts {
			out = append(out, Observation{ReportID: EvidenceID(evidence + "/" + harness + "/" + id + "/" + effort), Harness: harness, Model: id, Effort: effort, Status: "advertised"})
		}
	}
	return out
}
