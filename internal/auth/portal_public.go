// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
)

// Catalog, release history, roadmap, llms.txt, catalog.json and roadmap.json
// accept HEAD because net/http serves HEAD for a registered GET. Wishes, votes
// and corrections are POST only; other methods stay authenticated.
const (
	portalCatalogPattern     = "GET /api/public/portal/{tenantSlug}"
	portalCatalogFilePattern = "GET /api/public/portal/{tenantSlug}/catalog.json"
	portalLlmsPattern        = "GET /api/public/portal/{tenantSlug}/llms.txt"
	portalReleasesPattern    = "GET /api/public/portal/{tenantSlug}/releases"
	portalRoadmapPattern     = "GET /api/public/portal/{tenantSlug}/roadmap"
	portalRoadmapFilePattern = "GET /api/public/portal/{tenantSlug}/roadmap.json"
	portalWishPattern        = "POST /api/public/portal/{tenantSlug}/wishes"
	portalVotePattern        = "POST /api/public/portal/{tenantSlug}/wishes/{wishKey}/votes"
	portalCorrectionPattern  = "POST /api/public/portal/{tenantSlug}/corrections"
)

// Slug and wish key match the portal handlers. Anything else keeps the auth gate,
// including extra segments, trailing slashes, encoded slashes and dot segments.
var (
	portalSlugPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	portalWishKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}-[1-9][0-9]*$`)
)

// publicPortalRequest classifies a canonical portal URL for the customer
// allow helper. Session refresh does not call it.
func publicPortalRequest(r *http.Request) bool {
	kind := portalPublicKind(r)
	if kind == "" {
		return false
	}
	// Product routes must remain exact public declarations as well.
	if strings.Contains(r.URL.Path, "/products/") || strings.HasSuffix(r.URL.Path, "/participation") {
		return r.Pattern == "" || authz.PatternIsPublic(r.Pattern) && r.Pattern == portalCanonicalPattern(r)
	}
	switch r.Pattern {
	case "":
		return true
	case portalCatalogPattern:
		return kind == "read"
	case portalCatalogFilePattern:
		return kind == "catalog-file"
	case portalLlmsPattern:
		return kind == "llms"
	case portalReleasesPattern:
		return kind == "releases"
	case portalRoadmapPattern:
		return kind == "roadmap"
	case portalRoadmapFilePattern:
		return kind == "roadmap-file"
	case portalWishPattern:
		return kind == "wish"
	case portalVotePattern:
		return kind == "vote"
	case portalCorrectionPattern:
		return kind == "correction"
	default:
		return false
	}
}

func portalPublicKind(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	escaped := strings.ToLower(r.URL.EscapedPath())
	if strings.Contains(escaped, "%") || strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.Path, "//") || strings.HasSuffix(r.URL.Path, "/") {
		return ""
	}
	if r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path {
		return ""
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) >= 7 && parts[1] == "api" && parts[2] == "public" && parts[3] == "portal" && portalSlugPattern.MatchString(parts[4]) && parts[5] == "products" && portalSlugPattern.MatchString(parts[6]) {
		clone := *r
		u := *r.URL
		clone.URL = &u
		suffix := strings.Join(parts[7:], "/")
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && (suffix == "catalog" || suffix == "wishes" || suffix == "comparison" || suffix == "pace") {
			suffix = ""
		}
		u.Path = "/api/public/portal/" + parts[4]
		if suffix != "" {
			u.Path += "/" + suffix
		}
		u.RawPath = ""
		return portalPublicKind(&clone)
	}
	if len(parts) == 6 && parts[1] == "api" && parts[2] == "public" && parts[3] == "portal" && portalSlugPattern.MatchString(parts[4]) && parts[5] == "participation" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return "participation"
	}

	if len(parts) == 5 && parts[1] == "api" && parts[2] == "public" && parts[3] == "portal" && portalSlugPattern.MatchString(parts[4]) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			return "read"
		}
		return ""
	}
	if len(parts) == 6 && parts[1] == "api" && parts[2] == "public" && parts[3] == "portal" && portalSlugPattern.MatchString(parts[4]) {
		switch parts[5] {
		case "releases":
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				return "releases"
			}
		case "roadmap":
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				return "roadmap"
			}
		case "roadmap.json":
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				return "roadmap-file"
			}
		case "llms.txt":
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				return "llms"
			}
		case "catalog.json":
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				return "catalog-file"
			}
		}
	}
	if len(parts) == 6 && parts[1] == "api" && parts[2] == "public" && parts[3] == "portal" && parts[5] == "wishes" && portalSlugPattern.MatchString(parts[4]) {
		if r.Method == http.MethodPost {
			return "wish"
		}
		return ""
	}
	if len(parts) == 6 && parts[1] == "api" && parts[2] == "public" && parts[3] == "portal" && parts[5] == "corrections" && portalSlugPattern.MatchString(parts[4]) {
		if r.Method == http.MethodPost {
			return "correction"
		}
		return ""
	}
	if len(parts) == 8 && parts[1] == "api" && parts[2] == "public" && parts[3] == "portal" && parts[5] == "wishes" && parts[7] == "votes" && portalSlugPattern.MatchString(parts[4]) && len(parts[6]) <= 30 && portalWishKeyPattern.MatchString(parts[6]) {
		if r.Method == http.MethodPost {
			return "vote"
		}
	}
	return ""
}

// Build the one declaration matching a validated canonical path; arbitrary
// public patterns do not turn a different route into a portal route.
func portalCanonicalPattern(r *http.Request) string {
	parts := strings.Split(r.URL.Path, "/")
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	parts[4] = "{tenantSlug}"
	if len(parts) >= 7 && parts[5] == "products" {
		parts[6] = "{productSlug}"
	}
	for i := 5; i+1 < len(parts); i++ {
		if parts[i] == "wishes" && i+2 < len(parts) && parts[i+2] == "votes" {
			parts[i+1] = "{wishKey}"
		}
	}
	return method + " " + strings.Join(parts, "/")
}
