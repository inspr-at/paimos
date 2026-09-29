// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubRefusesRedirects(t *testing.T) {
	for name, location := range map[string]string{
		"loopback":        "http://127.0.0.1:12345/private",
		"http subdomain":  "http://child.api.github.com/private",
		"cross host":      "https://example.com/private",
		"https subdomain": "https://child.api.github.com/private",
		"same host":       "https://api.github.com/renamed",
	} {
		t.Run(name, func(t *testing.T) {
			for _, supplied := range []bool{false, true} {
				calls, leaked, overrideCalls := 0, false, 0
				transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" || calls > 1 {
						leaked = r.Header.Get("Authorization") != ""
						return nil, errors.New("unexpected redirected request")
					}
					if r.Header.Get("Authorization") != "Bearer "+fixtureToken {
						t.Fatal("fixture authorization did not reach GitHub")
					}
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				})
				gh := &GitHub{Token: fixtureToken}
				client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
					overrideCalls++
					return nil
				}}
				if supplied {
					gh.Client = client
				} else {
					original := http.DefaultTransport
					http.DefaultTransport = transport
					defer func() { http.DefaultTransport = original }()
				}
				reads := []func() error{
					func() error { _, err := gh.Commit(t.Context(), fixtureRepo, fixtureCommit); return err },
					func() error { _, err := gh.Tree(t.Context(), fixtureRepo, fixtureCommit); return err },
					func() error { _, err := gh.Blob(t.Context(), fixtureRepo, fixtureCommit, 12); return err },
				}
				for _, read := range reads {
					calls = 0
					err := read()
					if !errors.Is(err, ErrGit) || !strings.Contains(err.Error(), "302") || strings.Contains(err.Error(), location) {
						t.Fatalf("redirect must be refused without exposing Location: %v", err)
					}
					if calls != 1 || leaked || overrideCalls != 0 {
						t.Fatal("redirect was followed or authorization escaped")
					}
				}
				if supplied {
					// The caller's shared client must retain its own policy.
					_ = client.CheckRedirect(nil, nil)
					if overrideCalls != 1 {
						t.Fatal("shared client was mutated")
					}
				}
			}
		})
	}
}
