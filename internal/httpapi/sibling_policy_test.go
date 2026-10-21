// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// AEON-1495 risk: the app's connect-src blocks the owner's sibling reads, the
// widening reaches portal documents or the API, or it drops another policy.
func TestSiblingOriginsJoinConnectSrcOnAppDocumentsOnly(t *testing.T) {
	for _, aithema := range []string{"", "https://app.example.com"} {
		s := &Server{AttachmentSandboxOrigin: "https://preview.example.net", AithemaOrigin: aithema, SiblingOrigins: []string{"https://pma.example", "https://bad.example/path"}}
		for _, path := range []string{"/", "/p/AEON", "/portal/example", "/api/health"} {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			policy := w.Header().Get("Content-Security-Policy")
			app := !strings.HasPrefix(path, "/portal/") && !strings.HasPrefix(path, "/api/")
			if strings.Contains(policy, "https://pma.example") != app {
				t.Fatalf("sibling connect policy on %s: %q", path, policy)
			}
			if strings.Contains(policy, "bad.example") {
				t.Fatalf("an origin with a path widened the policy: %q", policy)
			}
			if !app {
				continue
			}
			if strings.Count(policy, "connect-src") != 1 || !strings.Contains(policy, "connect-src 'self'") || !strings.Contains(policy, "frame-src 'self' https://preview.example.net") {
				t.Fatalf("policies conflict on %s: %q", path, policy)
			}
			if aithema != "" && !strings.Contains(policy, "connect-src 'self' wss://app.example.com https://pma.example") {
				t.Fatalf("sibling policy lost the Aithema connection: %q", policy)
			}
		}
	}
	plain := &Server{}
	w := httptest.NewRecorder()
	plain.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src") {
		t.Fatal("no siblings still widened connect-src")
	}
}
