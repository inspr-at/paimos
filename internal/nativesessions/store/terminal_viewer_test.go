// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

func TestTerminalViewerURLStrictAllowlist(t *testing.T) {
	h, err := NewViewerHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"https://100.64.0.0/lead", "https://100.127.255.255/OPS-1", "https://mbp.example.ts.net/Tick_2.3", "https://mbp.ts.net:443/lead"} {
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
}
