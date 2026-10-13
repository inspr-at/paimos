// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type viewerHost struct {
	exact, suffix string
	prefix        netip.Prefix
}
type ViewerHosts struct{ hosts []viewerHost }

func NewViewerHosts(setting []string) (ViewerHosts, error) {
	if setting == nil {
		setting = []string{"100.64.0.0/10", "*.ts.net"}
	}
	if len(setting) > 64 {
		return ViewerHosts{}, fail(400, nativecontract.ReasonBufferFull)
	}
	out := ViewerHosts{}
	for _, value := range setting {
		if len(value) > 253 || value != strings.TrimSpace(value) || value == "" {
			return ViewerHosts{}, fail(400, nativecontract.ReasonInvalidInput)
		}
		value = strings.ToLower(value)
		h := viewerHost{}
		if p, err := netip.ParsePrefix(value); err == nil {
			h.prefix = p.Masked()
		} else if strings.HasPrefix(value, "*.") && validHost(value[2:]) {
			h.suffix = value[1:]
		} else if validHost(value) {
			h.exact = value
		} else {
			return ViewerHosts{}, fail(400, nativecontract.ReasonInvalidInput)
		}
		out.hosts = append(out.hosts, h)
	}
	return out, nil
}

func validHost(s string) bool {
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip.IsValid() && !ip.Is4In6()
	}
	if len(s) > 253 || strings.HasSuffix(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Validate never echoes an unsafe URL. The only URL path is one Zellij session
// name; no login token, bearer, userinfo, query or fragment can be stored.
func (h ViewerHosts) Validate(raw string) error {
	invalid := func() error { return fail(400, nativecontract.ReasonInvalidInput) }
	if len(raw) == 0 || len(raw) > 200 || strings.ContainsAny(raw, "?#\\ \t\r\n") {
		return invalid()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return invalid()
	}
	if u.Port() != "" && u.Port() != "443" {
		return invalid()
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(u.Host, ":") {
		return invalid()
	}
	if !validHost(host) || u.RawPath != "" || u.EscapedPath() != u.Path || !zellijPath.MatchString(u.Path) || u.Path == "/." || u.Path == "/.." {
		return invalid()
	}
	for _, entry := range h.hosts {
		if entry.exact != "" && host == entry.exact || entry.suffix != "" && strings.HasSuffix(host, entry.suffix) && len(host) > len(entry.suffix) {
			return nil
		}
		if ip, err := netip.ParseAddr(host); err == nil && entry.prefix.IsValid() && entry.prefix.Contains(ip) {
			return nil
		}
	}
	return invalid()
}

var zellijPath = regexp.MustCompile(`^/[A-Za-z0-9._-]{1,64}$`)
