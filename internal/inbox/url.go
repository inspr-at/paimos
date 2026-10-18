// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

var (
	errWebhookURL      = errors.New("webhook URL is not a public https address")
	errWebhookRedirect = errors.New("webhook redirect rejected")
)

// Blocks loopback, private, link-local, CGNAT (cloud metadata), documentation
// and other non-global ranges. Mixed DNS answers are rejected in allowAll.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
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
}

func validateWebhookURL(ctx context.Context, raw string) error {
	host, _, err := parseWebhook(raw)
	if err != nil {
		return err
	}
	if err := blockedName(host); err != nil {
		return err
	}
	ips, err := resolveIPs(ctx, host)
	if err != nil {
		return err
	}
	return allowAll(ips)
}

func parseWebhook(raw string) (host, port string, err error) {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, " \t\r\n\\") {
		return "", "", errWebhookURL
	}
	if !strings.HasPrefix(raw, "https://") {
		return "", "", errWebhookURL
	}
	u, err := http.NewRequest(http.MethodPost, raw, nil)
	if err != nil || u.URL.Scheme != "https" || u.URL.Host == "" || u.URL.User != nil || u.URL.Fragment != "" || u.URL.Opaque != "" {
		return "", "", errWebhookURL
	}
	host = strings.TrimSuffix(u.URL.Hostname(), ".")
	if host == "" || strings.Contains(host, "%") {
		return "", "", errWebhookURL
	}
	port = u.URL.Port()
	if port == "" {
		port = "443"
	} else {
		n, conv := strconv.Atoi(port)
		if conv != nil || n < 1 || n > 65535 {
			return "", "", errWebhookURL
		}
	}
	return host, port, nil
}

func blockedName(host string) error {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	switch h {
	case "", "localhost", "localhost.localdomain", "metadata", "metadata.google.internal",
		"metadata.internal", "instance-data", "instance-data.ec2.internal":
		return errWebhookURL
	}
	for _, suffix := range []string{".localhost", ".local", ".localdomain", ".internal", ".intranet", ".lan", ".home.arpa", ".corp", ".home", ".cluster.local"} {
		if strings.HasSuffix(h, suffix) {
			return errWebhookURL
		}
	}
	if strings.Contains(h, ".svc.") {
		return errWebhookURL
	}
	return nil
}

func resolveIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if ambiguousNumeric(host) {
		return nil, errWebhookURL
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errWebhookURL
	}
	return ips, nil
}

func ambiguousNumeric(host string) bool {
	// inet_aton-style addresses allow one to four decimal, octal or hex
	// components. A letter x inside a DNS label is not an address grammar.
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		digits := part
		hex := strings.HasPrefix(strings.ToLower(part), "0x")
		if hex {
			digits = part[2:]
			if digits == "" {
				return false
			}
		}
		for _, c := range digits {
			if c >= '0' && c <= '9' {
				continue
			}
			if hex && ((c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				continue
			}
			return false
		}
	}
	return net.ParseIP(host) == nil
}

func allowAll(ips []net.IP) error {
	if len(ips) == 0 {
		return errWebhookURL
	}
	for _, ip := range ips {
		if err := classifyIP(ip); err != nil {
			return err
		}
	}
	return nil
}

func classifyIP(ip net.IP) error {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return errWebhookURL
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsPrivate() || !addr.IsGlobalUnicast() {
		return errWebhookURL
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return errWebhookURL
		}
	}
	return nil
}

func dialPublic(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errWebhookURL
	}
	host = strings.TrimSuffix(host, ".")
	if err := blockedName(host); err != nil {
		return nil, err
	}
	ips, err := resolveIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	if err := allowAll(ips); err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var first error
	for _, ip := range ips {
		if !ipMatches(network, ip) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if first == nil {
			first = err
		}
	}
	if first == nil {
		return nil, errWebhookURL
	}
	return nil, first
}

func ipMatches(network string, ip net.IP) bool {
	switch network {
	case "tcp4":
		return ip.To4() != nil
	case "tcp6":
		return ip.To4() == nil
	default:
		return true
	}
}

func newWebhookClient(dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errWebhookRedirect
		},
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dial,
			ForceAttemptHTTP2:     false,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
			ExpectContinueTimeout: time.Second,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
	}
}

var webhookClient = newWebhookClient(dialPublic)

type safePoster struct{}

func (safePoster) Post(ctx context.Context, webhookURL string, body []byte) (int, error) {
	if err := validateWebhookURL(ctx, webhookURL); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "aeon-inbox-wake")
	resp, err := webhookClient.Do(req)
	if resp != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return resp.StatusCode, errWebhookRedirect
		}
		return 0, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, errWebhookRedirect
	}
	return resp.StatusCode, nil
}
