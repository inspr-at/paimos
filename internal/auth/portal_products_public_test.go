// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProductPortalAllowlistIsExact(t *testing.T) {
	base := "/api/public/portal/harbour/products/office"
	for _, suffix := range []string{"", "/catalog", "/wishes", "/comparison", "/pace", "/participation", "/catalog.json", "/llms.txt", "/releases", "/roadmap", "/roadmap.json"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			req := httptest.NewRequest(method, base+suffix, nil)
			if !publicPortalRequest(req) {
				t.Fatalf("public %s %s", method, req.URL.Path)
			}
			req.Pattern = portalCanonicalPattern(req)
			if !publicPortalRequest(req) {
				t.Fatalf("matched public %s", req.Pattern)
			}
			req.Pattern = "GET /api/public/portal/{tenantSlug}"
			if publicPortalRequest(req) {
				t.Fatalf("wrong pattern allowed %s", req.URL.Path)
			}
		}
	}
	for _, suffix := range []string{"/wishes", "/corrections", "/wishes/PWS-1/votes"} {
		req := httptest.NewRequest(http.MethodPost, base+suffix, nil)
		req.Pattern = portalCanonicalPattern(req)
		if !publicPortalRequest(req) {
			t.Fatalf("public post %s", req.Pattern)
		}
	}
	for _, entry := range []struct{ method, path string }{
		{"GET", base + "/products/another"}, {"GET", base + "/"}, {"GET", base + "/admin"},
		{"GET", base + "/wishes/PWS-1/votes"}, {"POST", base + "/releases"}, {"DELETE", base},
		{"GET", "/api/public/portal/harbour/products/office%2fprivate"},
		{"GET", "/api/public/portal/harbour/products/Office"},
		{"POST", base + "/wishes/invalid/votes"},
	} {
		req := httptest.NewRequest(entry.method, entry.path, nil)
		if publicPortalRequest(req) {
			t.Fatalf("noncanonical path public: %s %s", entry.method, entry.path)
		}
	}
}
