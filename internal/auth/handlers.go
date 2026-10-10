// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/identity"
	"github.com/inspr-at/paimos/internal/tenant"
)

var (
	errShortKey          = errors.New("session key must be at least 32 bytes")
	errOIDCNotConfigured = errors.New("oidc is not configured")
)

func (m *Module) homeURL() string {
	if m.cfg.PublicURL == "" {
		return "/"
	}
	return m.cfg.PublicURL + "/"
}

func (m *Module) handleLogin(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("tenant"))
	if slug == "" {
		slug = m.cfg.BootstrapTenantSlug
	}
	if _, err := m.tenantBySlug(r.Context(), slug); err != nil {
		if slug == m.cfg.BootstrapTenantSlug && errors.Is(err, pgx.ErrNoRows) {
			writeHTML(w, http.StatusInternalServerError, notReadyPage)
		} else {
			writeHTML(w, http.StatusBadRequest, signInFailedPage)
		}
		return
	}
	_, oc, err := m.oidcProvider(r.Context())
	if err != nil {
		writeHTML(w, http.StatusInternalServerError, notReadyPage)
		return
	}
	state, err := randomString(16)
	if err != nil {
		writeHTML(w, http.StatusInternalServerError, notReadyPage)
		return
	}
	nonce, err := randomString(16)
	if err != nil {
		writeHTML(w, http.StatusInternalServerError, notReadyPage)
		return
	}
	verifier := oauth2.GenerateVerifier()
	invite := strings.TrimSpace(r.URL.Query().Get("invite"))
	if len(invite) > 128 || strings.ContainsAny(invite, " \t\r\n") {
		invite = ""
	}
	payload := oidcPayload{State: state, Nonce: nonce, Verifier: verifier, Tenant: slug, Invite: invite, Exp: time.Now().Add(oidcTTL).Unix()}
	if err := m.setOIDCCookie(w, payload); err != nil {
		writeHTML(w, http.StatusInternalServerError, notReadyPage)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, oc.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// callbackFailure uses only internal codes/reasons: provider responses may contain
// credentials or user-controlled text and must never enter URLs or logs.
func (m *Module) callbackFailure(w http.ResponseWriter, r *http.Request, code, reason string) {
	m.clearOIDCCookie(w)
	slog.WarnContext(r.Context(), "OIDC callback failed", "error", code, "reason", reason, "request_id", httpapi.RequestID(r.Context()))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/signin?error="+code, http.StatusFound)
}

func (m *Module) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Sign-in touches only workspace rows; a cookie already present must not
	// change what it sees (ADR-003 P2).
	r = r.WithContext(db.NoProjects(r.Context(), "sign-in"))
	q := r.URL.Query()
	fail := func(code, reason string) { m.callbackFailure(w, r, code, reason) }
	switch q.Get("error") {
	case "":
	case "access_denied":
		fail("denied", "provider_denied")
		return
	case "server_error", "temporarily_unavailable":
		fail("unavailable", "provider_unavailable")
		return
	default:
		fail("failed", "provider_error")
		return
	}
	payload, err := m.readOIDCCookie(r)
	if err != nil || !hmacEqual(payload.State, q.Get("state")) {
		fail("expired", "invalid_state_cookie")
		return
	}
	if q.Get("code") == "" {
		fail("failed", "missing_code")
		return
	}
	if payload.StepUp != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
	}
	provider, oc, err := m.oidcProvider(r.Context())
	if err != nil {
		fail("unavailable", "provider_discovery")
		return
	}
	tok, err := oc.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(payload.Verifier))
	if err != nil {
		var response *oauth2.RetrieveError
		var network net.Error
		if errors.As(err, &network) || (errors.As(err, &response) &&
			(response.ErrorCode == "server_error" || response.ErrorCode == "temporarily_unavailable" ||
				(response.Response != nil && response.Response.StatusCode >= 500))) {
			fail("unavailable", "token_endpoint_unavailable")
		} else {
			fail("failed", "token_exchange")
		}
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idt, err := provider.Verifier(&oidc.Config{ClientID: m.cfg.OIDCClientID}).Verify(r.Context(), rawID)
	if err != nil {
		var expired *oidc.TokenExpiredError
		if errors.As(err, &expired) {
			fail("expired", "id_token_expired")
		} else {
			fail("failed", "id_token_verification")
		}
		return
	}
	if !hmacEqual(idt.Nonce, payload.Nonce) {
		fail("expired", "nonce_mismatch")
		return
	}
	if idt.Subject == "" {
		fail("failed", "missing_subject")
		return
	}
	if payload.StepUp != nil {
		m.finishStepUp(w, r, payload, idt)
		return
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Preferred     string `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil {
		fail("failed", "invalid_claims")
		return
	}
	display := firstNonEmpty(claims.Name, claims.Preferred, claims.Email, idt.Subject)
	tenantID, err := m.tenantBySlug(r.Context(), payload.Tenant)
	if err != nil {
		fail("unavailable", "tenant_lookup")
		return
	}
	principal, identityID, err := m.resolveOIDCPerson(r.Context(), tenantID, payload.Tenant, idt.Issuer, idt.Subject, claims.Email, display, claims.EmailVerified, payload.Invite)
	if errors.Is(err, errImportedNotMember) {
		fail("imported_account", "tenant_membership")
		return
	}
	if errors.Is(err, errNotMember) {
		fail("not_member", "tenant_membership")
		return
	}
	if err != nil {
		fail("unavailable", "principal_resolution")
		return
	}
	token, err := m.startSession(r.Context(), identityID, principal.TenantID, principal.ID)
	if err != nil {
		fail("unavailable", "session_creation")
		return
	}
	m.clearOIDCCookie(w)
	m.setSessionCookie(w, token)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, m.homeURL(), http.StatusFound)
}

func (m *Module) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if raw, err := decodeSessionToken(c.Value); err == nil {
			if err := m.deleteSession(r.Context(), raw); err != nil {
				writeInternal(w)
				return
			}
		}
	}
	m.clearSessionCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleMe(w http.ResponseWriter, r *http.Request) {
	// This authenticated self-service endpoint has no role or key-scope gate.
	// loadMe always selects the caller from the trusted authentication context.
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		m.writeMeUnauthorized(w)
		return
	}
	view, err := m.loadMe(r.Context(), p)
	if errors.Is(err, pgx.ErrNoRows) {
		m.writeMeUnauthorized(w)
		return
	}
	if err != nil {
		writeInternal(w)
		return
	}
	writeJSON(w, http.StatusOK, m.meJSONFrom(view))
}

func (m *Module) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	// Sign-in touches only workspace rows; a cookie already present must not
	// change what it sees (ADR-003 P2).
	r = r.WithContext(db.NoProjects(r.Context(), "sign-in"))
	if !m.cfg.Dev() {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Email  string `json:"email"`
		Tenant string `json:"tenant"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	email := strings.TrimSpace(body.Email)
	if !validEmail(email) {
		writeBadRequest(w, "email is required")
		return
	}
	slug := strings.TrimSpace(body.Tenant)
	if slug == "" {
		slug = m.cfg.BootstrapTenantSlug
	}
	tenantID, err := m.tenantBySlug(r.Context(), slug)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusInternalServerError, errorJSON{Error: "workspace is not ready"})
		return
	}
	if err != nil {
		writeInternal(w)
		return
	}
	principal, identityID, err := m.personByEmail(r.Context(), tenantID, email)
	if errors.Is(err, pgx.ErrNoRows) {
		if slug != m.cfg.BootstrapTenantSlug || !adminEmail(email, m.cfg.BootstrapAdminEmail) {
			writeJSON(w, http.StatusForbidden, errorJSON{Error: notMemberSentence})
			return
		}
		identityID, err = m.upsertIdentity(r.Context(), tenantID, devIssuer, identity.FoldEmailASCII(email), email, email)
		if err != nil {
			writeInternal(w)
			return
		}
		principal, err = m.ensurePerson(r.Context(), tenantID, identityID, email, email)
	}
	if errors.Is(err, errNotMember) {
		writeJSON(w, http.StatusForbidden, errorJSON{Error: notMemberSentence})
		return
	}
	if err != nil {
		writeInternal(w)
		return
	}
	token, err := m.startSession(r.Context(), identityID, principal.TenantID, principal.ID)
	if err != nil {
		writeInternal(w)
		return
	}
	m.setSessionCookie(w, token)
	view, err := m.loadMe(r.Context(), principal)
	if err != nil {
		writeInternal(w)
		return
	}
	writeJSON(w, http.StatusOK, m.meJSONFrom(view))
}

func hmacEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func validEmail(s string) bool {
	if s == "" || len(s) > 320 || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return strings.Count(s, "@") == 1 && !strings.HasPrefix(s, "@") && !strings.HasSuffix(s, "@")
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeBadRequest(w, "bad request")
		return false
	}
	return true
}
