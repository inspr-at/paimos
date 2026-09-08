// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/publicbase"
)

// Canonical cookie names are paimos-owned so a shared origin cannot
// collide with Aithema/Pharos/Janus. Legacy names exist only for
// standalone dual-read/dual-write. Shared-origin mounts never accept or
// emit the generic names — including on logout — so we do not clobber
// another app's cookies. Path=/ is not a security boundary; __Host-
// cookies, if ever used, keep Path=/ and are not weakened here.
const (
	sessionCookie       = "paimos_session"
	legacySessionCookie = "session"

	CSRFCookieName       = "paimos_csrf_token"
	legacyCSRFCookieName = "csrf_token"

	oidcStateCookie = "paimos_oidc_state"
	oidcVerifCookie = "paimos_oidc_pkce"
	oidcNonceCookie = "paimos_oidc_nonce"

	legacyOIDCStateCookie = "oidc_state"
	legacyOIDCVerifCookie = "oidc_pkce"
	legacyOIDCNonceCookie = "oidc_nonce"

	oidcReturnCookie       = "paimos_oidc_return"
	legacyOIDCReturnCookie = "oidc_return"
)

func sharedOriginCookies() bool {
	return publicbase.Current().SharedOrigin()
}

func readNamedCookie(r *http.Request, canonical, legacy string) (*http.Cookie, error) {
	if c, err := r.Cookie(canonical); err == nil {
		return c, nil
	}
	if sharedOriginCookies() {
		return nil, http.ErrNoCookie
	}
	return r.Cookie(legacy)
}

func readSessionCookie(r *http.Request) (*http.Cookie, error) {
	return readNamedCookie(r, sessionCookie, legacySessionCookie)
}

func readCSRFCookie(r *http.Request) (*http.Cookie, error) {
	return readNamedCookie(r, CSRFCookieName, legacyCSRFCookieName)
}

func readOIDCCookie(r *http.Request, canonical, legacy string) (*http.Cookie, error) {
	return readNamedCookie(r, canonical, legacy)
}

func cookieWriteNames(canonical, legacy string) []string {
	if sharedOriginCookies() {
		return []string{canonical}
	}
	return []string{canonical, legacy}
}

func setHTTPCookie(w http.ResponseWriter, name string, cookie http.Cookie) {
	cookie.Name = name
	http.SetCookie(w, &cookie)
}

func setSessionCookieValue(w http.ResponseWriter, value string, expires time.Time) {
	// #nosec G124 -- HttpOnly + SameSite=Lax; Secure mirrors COOKIE_SECURE.
	base := http.Cookie{
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
	for _, name := range cookieWriteNames(sessionCookie, legacySessionCookie) {
		setHTTPCookie(w, name, base)
	}
}

func expireNamedCookies(w http.ResponseWriter, names ...string) {
	for _, name := range names {
		// #nosec G124 -- deletion cookie (empty value, MaxAge -1).
		http.SetCookie(w, &http.Cookie{
			Name:    name,
			Value:   "",
			Path:    "/",
			Expires: time.Unix(0, 0),
			MaxAge:  -1,
		})
	}
}

func clearSessionCookies(w http.ResponseWriter) {
	expireNamedCookies(w, sessionCookie)
	if !sharedOriginCookies() {
		expireNamedCookies(w, legacySessionCookie)
	}
	ClearCSRFCookie(w)
}

// browserPath joins an app-rooted path with the configured native prefix.
// Fully-qualified http(s) URLs are left unchanged (operator-owned).
func browserPath(appPath string) string {
	trimmed := strings.TrimSpace(appPath)
	if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") {
		return trimmed
	}
	return publicbase.Current().Join(trimmed)
}

func safeOIDCReturnPath(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return ""
	}
	if parsed.Path == "/login" || strings.HasPrefix(parsed.Path, "/login/") {
		return ""
	}
	return publicbase.Current().Join(raw)
}
