// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-743 — home realm discovery for the identifier-first login.
//
// The login page asks for the identifier first, then shows only the
// method(s) that apply. This endpoint answers "which methods?" for one
// identifier.
//
// INV-AUTH-HRD (the invariant that shapes this whole file): the answer
// is derived from OPERATOR CONFIGURATION ONLY — never from a lookup of
// the identifier. A per-account answer would turn this public endpoint
// into a user-enumeration oracle, which the rest of the auth surface
// deliberately avoids (LoginHandler answers "invalid credentials" for
// unknown user and wrong password alike; ForgotPassword answers 202
// even for malformed input). So: no DB access here, ever. The reply
// for a nonexistent account is byte-identical to the reply for a real
// one with the same email domain.
//
// Consequence worth stating plainly: this endpoint is a UI HINT. It
// never gates authentication. POST /api/auth/login keeps accepting a
// password for any account — including one whose domain routes to SSO
// — so a break-glass local admin is never locked out (the SPA also
// takes ?method=password to skip routing entirely).

package auth

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// envSSODomains is the operator's home-realm map: a comma-separated
// list of email domains served by the configured IdP. Unset (the
// default) means "no domain routing" — every identifier is offered
// password plus SSO, exactly as the one-step login did before PAI-743.
const envSSODomains = "OIDC_SSO_DOMAINS"

// loginMethods is the response shape. Password and SSO are independent
// booleans rather than one enum because "both" is the common case and
// a future second IdP would add a field, not change the semantics.
type loginMethods struct {
	Password bool   `json:"password"`
	SSO      bool   `json:"sso"`
	SSOLabel string `json:"sso_label,omitempty"`
}

// ssoDomains parses OIDC_SSO_DOMAINS into a lowercase set. Entries are
// trimmed and may be written with or without a leading "@".
func ssoDomains() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv(envSSODomains))
	if raw == "" {
		return nil
	}
	out := map[string]struct{}{}
	for part := range strings.SplitSeq(raw, ",") {
		d := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "@")))
		if d != "" {
			out[d] = struct{}{}
		}
	}
	return out
}

// identifierDomain returns the lowercase domain of an email-shaped
// identifier, or "" for a bare username. Uses the LAST "@" so that
// local parts containing "@" (legal, if rare) resolve correctly.
func identifierDomain(identifier string) string {
	id := strings.ToLower(strings.TrimSpace(identifier))
	at := strings.LastIndex(id, "@")
	if at < 0 || at == len(id)-1 {
		return ""
	}
	return id[at+1:]
}

// resolveLoginMethods applies the routing rules. Pure function of
// (identifier, ssoEnabled, domain config) — no I/O, so the table test
// covers the whole decision surface.
//
//   - SSO off               → password only (routing is irrelevant).
//   - domain in the list    → SSO only; the password field is hidden
//     because that realm's credentials live at the IdP.
//   - anything else         → password + SSO, i.e. the pre-PAI-743
//     behaviour. A bare username lands here on purpose: we cannot know
//     which realm it belongs to without a lookup, and guessing wrong
//     would strand the user.
func resolveLoginMethods(identifier string, ssoEnabled bool, domains map[string]struct{}) loginMethods {
	if !ssoEnabled {
		return loginMethods{Password: true}
	}
	label := envDefault("OIDC_BUTTON_LABEL", "Sign in with SSO")
	if d := identifierDomain(identifier); d != "" && len(domains) > 0 {
		if _, ok := domains[d]; ok {
			return loginMethods{Password: false, SSO: true, SSOLabel: label}
		}
	}
	return loginMethods{Password: true, SSO: true, SSOLabel: label}
}

// LoginMethods — POST /api/auth/login/methods  {"identifier": "..."}
//
// Public by necessity: it runs before any session exists. Safe to be
// public because of INV-AUTH-HRD above — it reveals only which realms
// the operator configured, which the person typing the address already
// knows.
func LoginMethods(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identifier string `json:"identifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	cfg, err := loadOIDCConfig(r.Context())
	ssoEnabled := err == nil && cfg.AuthorizationEndpoint != ""

	w.Header().Set("Content-Type", "application/json")
	// The identifier is a credential-adjacent value typed by the user;
	// no-store keeps it (and the routing answer) out of shared caches.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resolveLoginMethods(body.Identifier, ssoEnabled, ssoDomains()))
}
