// SPDX-License-Identifier: AGPL-3.0-only

package modelprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/embedding"
)

// Endpoint selection belongs only to settings.manage persons. Loopback and LAN
// are intentional for local models. Block special-use metadata addresses, dial
// only validated resolved addresses, bypass ambient proxies, and refuse redirects.
var blockedProviderPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("168.63.129.16/32"), // Azure platform/metadata endpoint.
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("fd00:ec2::254/128"), // AWS IPv6 metadata inside ULA.
}

func allowedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	for _, prefix := range blockedProviderPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func endpoint(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || len(base) > 2048 || strings.TrimSpace(base) != base || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("invalid provider base URL")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !allowedIP(ip) {
		return "", errors.New("invalid provider address")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + path
	u.RawPath = ""
	return u.String(), nil
}

func newClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.MaxConnsPerHost = 2
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("provider address unavailable")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("provider address unavailable")
		}
		for _, ip := range ips {
			if !allowedIP(ip) {
				return nil, errors.New("provider address refused")
			}
		}
		var dialer net.Dialer
		dialer.Timeout = 10 * time.Second
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("provider connection failed")
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Completion struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
}

// Chat rechecks the immutable request binding and workspace feature before
// sending anything; missing or disabled configurations have no fallback.
func (s *Service) Chat(ctx context.Context, tenantID, providerID string, revision int64, messages []Message) (Completion, error) {
	c, key, err := s.resolve(ctx, tenantID)
	if err != nil {
		return Completion{}, err
	}
	if !c.Enabled || !c.Features.CRMNoteRewrite {
		return Completion{}, ErrDisabled
	}
	if c.ProviderID != providerID || c.Revision != revision {
		return Completion{}, errors.New("provider configuration changed")
	}
	return s.chat(ctx, c, key, messages, 2048)
}

func (s *Service) chat(ctx context.Context, c Config, key Secret, messages []Message, maxTokens int) (Completion, error) {
	endpointURL, err := endpoint(c.BaseURL, "chat/completions")
	if err != nil || c.ChatModel == "" {
		return Completion{}, errors.New("provider is unconfigured")
	}
	body, err := json.Marshal(struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens"`
		Stream    bool      `json:"stream"`
	}{c.ChatModel, messages, maxTokens, false})
	if err != nil || len(body) > 128<<10 {
		return Completion{}, errors.New("provider request is too large")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return Completion{}, errors.New("provider request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+string(key))
	}
	res, err := s.client.Do(req)
	if err != nil {
		return Completion{}, errors.New("provider request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("provider endpoint status %d", res.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(payload) > 1<<20 {
		return Completion{}, errors.New("invalid provider response")
	}
	var parsed struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Input  int64 `json:"prompt_tokens"`
			Output int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(payload, &parsed) != nil || len(parsed.Choices) != 1 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" || len(parsed.Choices[0].Message.Content) > 20000 || parsed.Usage.Input < 0 || parsed.Usage.Output < 0 || parsed.Choices[0].FinishReason == "length" {
		return Completion{}, errors.New("invalid provider response")
	}
	return Completion{parsed.Choices[0].Message.Content, parsed.Usage.Input, parsed.Usage.Output}, nil
}

type workspaceEmbedding struct {
	embedding.Provider
	identity string
}

func (p workspaceEmbedding) Model() string { return p.identity }

// Embeddings is the same resolver for query and indexing. Nil leaves a tenant's
// queue untouched and its search lexical. Stored vector identity includes the
// endpoint and model, so a provider switch never mixes incompatible vectors.
func (s *Service) Embeddings(ctx context.Context, tenantID string) (embedding.Provider, error) {
	c, key, err := s.resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !c.Enabled || !c.Features.Embeddings {
		return nil, nil
	}
	u, err := endpoint(c.BaseURL, "embeddings")
	if err != nil {
		return nil, err
	}
	p, err := embedding.NewHTTPProvider(embedding.HTTPConfig{URL: u, Model: c.EmbeddingModel, APIKey: string(key), Client: s.client})
	if err != nil {
		return nil, errors.New("embedding provider unavailable")
	}
	sum := sha256.Sum256([]byte(c.ProviderID + "\x00" + u + "\x00" + c.EmbeddingModel))
	return workspaceEmbedding{Provider: p, identity: "workspace/" + hex.EncodeToString(sum[:])}, nil
}
