// SPDX-License-Identifier: AGPL-3.0-only
package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/workerlease"
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
// Go would copy Authorization automatically.
func TestWorkerProofHeadersRefuseRedirects(t *testing.T) {
	const lease = "synthetic-only-proof-padding-000000"
	for _, name := range []string{"scheme", "legacy"} {
		t.Run(name, func(t *testing.T) {
			redirected := false
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/destination" {
					redirected = true
					return
				}
				switch name {
				case "scheme":
					if r.Header.Get("Authorization") != workerlease.Value("synthetic-agent", lease) || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get(workerlease.LegacyHeader) != "" {
						t.Error("worker proof left Authorization")
					}
				default:
					if r.Header.Get("Authorization") != "Bearer synthetic-agent" || r.Header.Get(workerlease.LegacyHeader) != lease {
						t.Error("legacy proof was rewritten")
					}
				}
				http.Redirect(w, r, "/destination", http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			c := New(source.URL, "synthetic-agent")
			// Use a client that would otherwise follow redirects.
			c.HTTP = &http.Client{}
			headers := map[string]string{workerlease.LegacyHeader: lease}
			if name == "scheme" {
				headers = map[string]string{"Authorization": workerlease.Value("synthetic-agent", lease)}
			}
			if err := c.DoWithHeaders(t.Context(), http.MethodPost, "/worker", nil, nil, headers); err == nil {
				t.Fatal("redirect reported success")
			}
			if redirected {
				t.Fatal("worker proof followed redirect")
			}
		})
	}
}

// Risk: a reverse proxy reflects private proof into an error that agentd logs.
func TestWorkerProofHeadersRedactReflectedFailures(t *testing.T) {
	lease := strings.Repeat("synthetic-7b", 4)
	for _, name := range []string{"scheme", "legacy"} {
		for _, escaped := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/plain", true: "/escaped"}[escaped], func(t *testing.T) {
				headers := map[string]string{workerlease.LegacyHeader: lease}
				reflected := workerlease.LegacyHeader + ": " + lease
				if name == "scheme" {
					headers = map[string]string{"Authorization": workerlease.Value("synthetic-agent", lease)}
					reflected = headers["Authorization"]
				}
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusForbidden)
					raw, _ := json.Marshal(map[string]string{"error": "proxy reflected " + reflected, "reason_code": lease, "attach_refusal": lease})
					if escaped {
						raw = []byte(strings.ReplaceAll(string(raw), "synthetic", "\\u0073ynthetic"))
					}
					_, _ = w.Write(raw)
				}))
				defer source.Close()
				err := New(source.URL, "synthetic-agent").DoWithHeaders(t.Context(), http.MethodPost, "/worker", nil, nil, headers)
				var status *StatusError
				if !errors.As(err, &status) || status.Status != http.StatusForbidden {
					t.Fatal("lost status classification")
				}
				if strings.Contains(err.Error(), lease) || strings.Contains(status.ReasonCode, lease) || strings.Contains(status.AttachRefusal, lease) || !strings.Contains(err.Error(), "[redacted]") {
					t.Fatal("private proof escaped refusal redaction")
				}
			})
		}
	}
}
