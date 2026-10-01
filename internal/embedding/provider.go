// SPDX-License-Identifier: AGPL-3.0-only

package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Provider computes one model of 1536-dimensional embeddings.
// Search and the queue must share a provider so stored and query vectors match.
type Provider interface {
	Model() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Resolver selects a provider per tenant; nil means this tenant is disabled.
type Resolver func(context.Context, string) (Provider, error)

// HTTPConfig is an OpenAI-compatible embeddings endpoint.
type HTTPConfig struct {
	URL    string
	Model  string
	APIKey string
	Client *http.Client
}

// HTTPProvider posts {"model","input"} and reads data[].embedding.
type HTTPProvider struct {
	url    string
	model  string
	apiKey string
	client *http.Client
}

// FromEnv builds a provider from AEON_EMBEDDING_*. An empty URL returns
// (nil, nil): lexical search only, and the queue stays idle until configured.
func FromEnv() (Provider, error) {
	raw := strings.TrimSpace(os.Getenv("AEON_EMBEDDING_URL"))
	if raw == "" {
		return nil, nil
	}
	model := strings.TrimSpace(os.Getenv("AEON_EMBEDDING_MODEL"))
	if model == "" {
		model = "text-embedding-3-small"
	}
	return NewHTTPProvider(HTTPConfig{
		URL:    raw,
		Model:  model,
		APIKey: os.Getenv("AEON_EMBEDDING_API_KEY"),
	})
}

// NewHTTPProvider checks the endpoint and model. The API key is sent as a
// bearer token and is never included in errors.
func NewHTTPProvider(cfg HTTPConfig) (*HTTPProvider, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("AEON_EMBEDDING_URL must be an http(s) URL")
	}
	if u.User != nil {
		return nil, errors.New("AEON_EMBEDDING_URL must not include userinfo")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" || len(model) > 200 {
		return nil, errors.New("embedding model is required")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPProvider{url: u.String(), model: model, apiKey: cfg.APIKey, client: client}, nil
}

// Model is the name stored on node_embeddings and passed to aeon_search_nodes.
func (p *HTTPProvider) Model() string { return p.model }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed requests one vector per text, in input order.
func (p *HTTPProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: p.model, Input: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return nil, errors.New("embedding request failed")
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, errors.New("embedding request failed")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding endpoint status %d", res.StatusCode)
	}
	var parsed embedResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, errors.New("embedding response was not JSON")
	}
	return arrange(texts, parsed)
}

func arrange(texts []string, parsed embedResponse) ([][]float32, error) {
	if len(parsed.Data) != len(texts) {
		return nil, errors.New("embedding response was incomplete")
	}
	out := make([][]float32, len(texts))
	indexed := false
	for _, item := range parsed.Data {
		if item.Index != 0 {
			indexed = true
			break
		}
	}
	if !indexed {
		for i, item := range parsed.Data {
			if err := Validate(item.Embedding); err != nil {
				return nil, err
			}
			out[i] = item.Embedding
		}
		return out, nil
	}
	seen := make([]bool, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(texts) || seen[item.Index] {
			return nil, errors.New("embedding response index")
		}
		if err := Validate(item.Embedding); err != nil {
			return nil, err
		}
		seen[item.Index] = true
		out[item.Index] = item.Embedding
	}
	return out, nil
}
