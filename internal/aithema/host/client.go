// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// servicePolicy is deployment-owned and deliberately absent from Settings.
// Both settings writes and dials use addresses; DNS answers never authorize
// loopback/private access unless the exact operator host:port was provisioned.
type servicePolicy struct {
	operatorLocal map[string]bool
	resolve       func(context.Context, string) ([]net.IPAddr, error)
}

func exactAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port || host == "" || strings.ContainsAny(host, "%*?/\\@# \t\r\n") {
		return "", errors.New("invalid operator-local service configuration")
	}
	if net.ParseIP(host) == nil {
		if len(host) > 253 {
			return "", errors.New("invalid operator-local service configuration")
		}
		for _, label := range strings.Split(host, ".") {
			if !dnsLabel.MatchString(label) {
				return "", errors.New("invalid operator-local service configuration")
			}
		}
	}
	return net.JoinHostPort(strings.ToLower(host), port), nil
}

var dnsLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func newServicePolicy(addresses []string) (servicePolicy, error) {
	p := servicePolicy{operatorLocal: map[string]bool{}}
	if len(addresses) > 32 {
		return p, errors.New("invalid operator-local service configuration")
	}
	for _, address := range addresses {
		key, err := exactAddress(address)
		if err != nil || p.operatorLocal[key] {
			return p, errors.New("invalid operator-local service configuration")
		}
		p.operatorLocal[key] = true
	}
	return p, nil
}

func serviceEndpoint(s Settings) (*url.URL, string, error) {
	u, err := url.Parse(s.ServiceURL)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(s.ServiceURL, "\r\n\t ") || (s.Location != "cloud" && s.Location != "operator") || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, "", errors.New("service unavailable")
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return nil, "", errors.New("service unavailable")
		}
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	address, err := exactAddress(net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, "", errors.New("service unavailable")
	}
	return u, address, nil
}

func (p servicePolicy) addresses(ctx context.Context, s Settings) ([]net.IPAddr, error) {
	u, address, err := serviceEndpoint(s)
	if err != nil {
		return nil, err
	}
	local := s.Location == "operator" && p.operatorLocal[address]
	if u.Scheme != "https" && !local {
		return nil, errors.New("service unavailable")
	}
	host, _, _ := net.SplitHostPort(address)
	resolve := p.resolve
	if resolve == nil {
		resolve = net.DefaultResolver.LookupIPAddr
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ips, err := resolve(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("service unavailable")
	}
	for _, ip := range ips {
		private := ip.IP.IsLoopback() || ip.IP.IsPrivate()
		if u.Scheme == "http" && !private || !publicIP(ip.IP) && !(local && private) {
			return nil, errors.New("service unavailable")
		}
	}
	return ips, nil
}

// No ambient HTTP proxy or redirects. Resolve/check every connection, then
// dial the checked IP literals so DNS cannot change between check and use.
func serviceTransport(s Settings, policies ...servicePolicy) *http.Transport {
	var p servicePolicy
	if len(policies) > 0 {
		p = policies[0]
	}
	return &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true, ResponseHeaderTimeout: 10 * time.Second, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, configured, err := serviceEndpoint(s)
		actual, addressErr := exactAddress(address)
		if err != nil || addressErr != nil || actual != configured {
			return nil, errors.New("service unavailable")
		}
		ips, err := p.addresses(ctx, s)
		if err != nil {
			return nil, err
		}
		_, port, _ := net.SplitHostPort(configured)
		dialer := net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("service unavailable")
	}}
}
func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
func serviceClient(s Settings, policies ...servicePolicy) *http.Client {
	return &http.Client{Transport: serviceTransport(s, policies...), Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
