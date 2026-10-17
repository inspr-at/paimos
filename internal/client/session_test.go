// SPDX-License-Identifier: AGPL-3.0-only
package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPersonSessionAuthenticationAndRedirectRefusal(t *testing.T) {
	const session = "synthetic-person-session"
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("aeon_session")
		if err != nil || cookie.Value != session || len(r.Header.Values("Authorization")) != 0 {
			t.Error("session request must contain only the person cookie")
		}
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := NewSession(source.URL, session)
	c.Token = "synthetic-unused-agent-token"
	if err := c.Do(t.Context(), http.MethodPost, "/reply", map[string]string{"body": "answer"}, nil); err == nil {
		t.Fatal("redirect reported success")
	}
	if redirected {
		t.Fatal("person session followed redirect")
	}
}

func TestAgentAuthenticationRemainsBearerOnly(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("agent authentication changed")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer source.Close()
	if err := New(source.URL, "synthetic-agent").Do(t.Context(), http.MethodPost, "/reply", nil, nil); err != nil {
		t.Fatal(err)
	}
}

// Worker proofs must not follow redirects, including to the same origin where
// Go normally copies headers such as Proxy-Authorization automatically.
func TestWorkerProofHeadersRefuseRedirects(t *testing.T) {
	for _, header := range []string{"Proxy-Authorization", "X-Aeon-Worker-Lease"} {
		t.Run(header, func(t *testing.T) {
			redirected := false
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/destination" {
					redirected = true
					return
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-agent" || r.Header.Get(header) == "" {
					t.Error("missing independent credentials")
				}
				http.Redirect(w, r, "/destination", http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			c := New(source.URL, "synthetic-agent")
			// Use a client that would otherwise follow redirects.
			c.HTTP = &http.Client{}
			if err := c.DoWithHeaders(t.Context(), http.MethodPost, "/worker", nil, nil, map[string]string{header: "Bearer synthetic-only-proof"}); err == nil {
				t.Fatal("redirect reported success")
			}
			if redirected {
				t.Fatal("worker proof followed redirect")
			}
		})
	}
}
