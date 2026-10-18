// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttachmentFramePolicyComposesWithAithema(t *testing.T) {
	for _, aithema := range []string{"", "https://app.example.com"} {
		s := &Server{AttachmentSandboxOrigin: "https://preview.example.net", AithemaOrigin: aithema}
		for _, path := range []string{"/", "/agents", "/p/AEON", "/portal/example", "/api/health"} {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			policy := w.Header().Get("Content-Security-Policy")
			want := !strings.HasPrefix(path, "/portal/") && !strings.HasPrefix(path, "/api/")
			if strings.Contains(policy, "https://preview.example.net") != want {
				t.Errorf("wrong frame policy on %s", path)
			}
			if want && strings.Count(policy, "frame-src") != 1 {
				t.Fatal("conflicting frame policies")
			}
			if want && aithema != "" && !strings.Contains(policy, "connect-src 'self' wss://app.example.com") {
				t.Fatal("attachment policy lost Aithema connection policy")
			}
		}
	}
}
