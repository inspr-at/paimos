// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type artifactTransport func(*http.Request) (*http.Response, error)

func (f artifactTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPreflightArtifactRedirectBoundsAndAuthority(t *testing.T) {
	// Risk: installation authority leaks to artifact storage, unsafe redirects
	// reach other hosts, or compressed artifact bytes are unbounded.
	location := "https://fixture.blob.core.windows.net/artifact?signature=fixture"
	oversized := false
	downloads := 0
	g := &GitHubApp{Config: AppConfig{Repository: "example/delivery"}, Client: &http.Client{Transport: artifactTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" {
			if r.Header.Get("Authorization") != "Bearer fixture-only-token" || r.URL.Path != "/repos/example/delivery/actions/artifacts/77/zip" {
				t.Fatal("unbound archive request")
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		downloads++
		if r.Header.Get("Authorization") != "" {
			t.Fatal("authority leaked to storage")
		}
		body := "zip fixture"
		if oversized {
			body = strings.Repeat("x", (64<<10)+1)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	if raw, err := g.artifactArchive(t.Context(), "fixture-only-token", 77); err != nil || string(raw) != "zip fixture" {
		t.Fatal("valid archive refused", err)
	}
	oversized = true
	if _, err := g.artifactArchive(t.Context(), "fixture-only-token", 77); err == nil {
		t.Fatal("oversized archive accepted")
	}
	for _, bad := range []string{"http://fixture.blob.core.windows.net/zip", "https://evil.example/zip", "https://user@fixture.blob.core.windows.net/zip", "https://fixture.blob.core.windows.net:8443/zip"} {
		location = bad
		before := downloads
		if _, err := g.artifactArchive(t.Context(), "fixture-only-token", 77); err == nil || before != downloads {
			t.Fatal("unsafe redirect followed")
		}
	}
}
