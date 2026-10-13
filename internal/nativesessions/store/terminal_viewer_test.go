// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTerminalViewerURLStrictAllowlist(t *testing.T) {
	h, err := NewViewerHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Risk: published Zellij links use 8082, but allowing that port must not
	// admit other destinations or login material.
	for _, url := range []string{"https://100.64.0.0/lead", "https://100.127.255.255/OPS-1", "https://mbp.example.ts.net/Tick_2.3", "https://mbp.ts.net:443/lead", "https://100.64.0.14:8082/LEAD", "https://box.ts.net:8082/LEAD"} {
		if err = h.Validate(url); err != nil {
			t.Fatalf("approved session rejected: %v", err)
		}
	}
	for name, url := range map[string]string{
		"http": "http://mbp.ts.net/lead", "scheme": "file:///lead", "outside-ip": "https://100.128.0.0/lead", "below-range": "https://100.63.255.255/lead",
		"outside-host": "https://example.org/lead", "wildcard-apex": "https://ts.net/lead", "suffix-lookalike": "https://mbp.ts.net.evil/lead",
		"userinfo": "https://person@mbp.ts.net/lead", "password": "https://person:secret@mbp.ts.net/lead",
		"query": "https://mbp.ts.net/lead?token=secret", "empty-query": "https://mbp.ts.net/lead?", "fragment": "https://mbp.ts.net/lead#token", "empty-fragment": "https://mbp.ts.net/lead#",
		"no-session": "https://mbp.ts.net/", "nested-path": "https://mbp.ts.net/lead/extra", "path-escape": "https://mbp.ts.net/%6cead",
		"session-too-long": "https://mbp.ts.net/" + strings.Repeat("a", 65), "session-space": "https://mbp.ts.net/lead session",
		"traversal": "https://mbp.ts.net/..", "backslash": "https://mbp.ts.net/lead\\name", "control": "https://mbp.ts.net/lead\n",
		"non-https-port": "https://mbp.ts.net:80/lead", "url-too-long": "https://" + strings.Repeat("a", 200) + ".ts.net/lead", "ipv6-mapped": "https://[::ffff:100.64.1.2]/lead",
		"unlisted-port": "https://box.ts.net:8083/LEAD", "zero-port": "https://box.ts.net:0/LEAD", "empty-port": "https://box.ts.net:/LEAD",
		"viewer-port-outside-host": "https://example.org:8082/LEAD", "viewer-port-userinfo": "https://person@box.ts.net:8082/LEAD",
		"viewer-port-query": "https://box.ts.net:8082/LEAD?token=secret", "viewer-port-fragment": "https://box.ts.net:8082/LEAD#token",
		"viewer-port-encoded-slash": "https://box.ts.net:8082/LEAD%2fextra", "viewer-port-nested-path": "https://box.ts.net:8082/LEAD/extra",
		"viewer-port-http": "http://box.ts.net:8082/LEAD", "opaque": "https:box.ts.net:8082/LEAD",
		"noncanonical-port": "https://box.ts.net:08082/LEAD", "port-too-large": "https://box.ts.net:65536/LEAD", "nonnumeric-port": "https://box.ts.net:viewer/LEAD",
	} {
		t.Run(name, func(t *testing.T) {
			err := h.Validate(url)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Reason != nativecontract.ReasonInvalidInput || strings.Contains(err.Error(), url) {
				t.Fatalf("unsafe URL did not fail with allowlisted reason: %v", err)
			}
		})
	}
	custom, err := NewViewerHosts([]string{"viewer.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if err = custom.Validate("https://viewer.example.org/lead"); err != nil {
		t.Fatal(err)
	}
	if err = custom.Validate("https://mbp.ts.net/lead"); err == nil {
		t.Fatal("setting did not replace default")
	}
	for _, tc := range []struct {
		name          string
		ports         []int
		accept, deny  []string
		failureReason nativecontract.Reason
	}{
		{name: "default-ports", accept: []string{"", ":443", ":8082"}, deny: []string{":8083"}},
		{name: "https-only", ports: []int{443}, accept: []string{"", ":443"}, deny: []string{":8082"}},
		{name: "viewer-only", ports: []int{8082}, accept: []string{":8082"}, deny: []string{"", ":443"}},
		{name: "custom-port", ports: []int{8443}, accept: []string{":8443"}, deny: []string{"", ":443", ":8082"}},
		{name: "empty-ports", ports: []int{}, deny: []string{"", ":443", ":8082"}},
		{name: "port-range", ports: []int{1, 65535}, accept: []string{":1", ":65535"}, deny: []string{":0", ":65536"}},
		{name: "negative-setting", ports: []int{-1}, failureReason: nativecontract.ReasonInvalidInput},
		{name: "zero-setting", ports: []int{0}, failureReason: nativecontract.ReasonInvalidInput},
		{name: "out-of-range-setting", ports: []int{65536}, failureReason: nativecontract.ReasonInvalidInput},
		{name: "oversized-setting", ports: make([]int, 65), failureReason: nativecontract.ReasonBufferFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Construction does not query the pool. Exercise the same options
			// wiring that Claim and person-only reads use for URL validation.
			s, err := New(&pgxpool.Pool{}, Options{
				Authority: fixedAuthority{}, Observer: &observedStart{},
				TerminalViewerHosts: []string{"viewer.example.org"}, TerminalViewerPorts: tc.ports,
			})
			if tc.failureReason != "" {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Status != 400 || failure.Reason != tc.failureReason {
					t.Fatalf("invalid port setting did not fail with expected reason: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, port := range tc.accept {
				if err := s.viewerHosts.Validate("https://viewer.example.org" + port + "/LEAD"); err != nil {
					t.Fatalf("configured port %q rejected: %v", port, err)
				}
				if err := s.viewerHosts.Validate("https://box.ts.net" + port + "/LEAD"); err == nil {
					t.Fatal("configured port bypassed host allowlist")
				}
			}
			for _, port := range tc.deny {
				err := s.viewerHosts.Validate("https://viewer.example.org" + port + "/LEAD")
				var failure *Failure
				if !errors.As(err, &failure) || failure.Status != 400 || failure.Reason != nativecontract.ReasonInvalidInput {
					t.Fatalf("unlisted port %q did not fail with invalid_input: %v", port, err)
				}
			}
		})
	}
}
