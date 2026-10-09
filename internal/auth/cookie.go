// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "aeon_session"
	oidcCookieName    = "aeon_oidc"
	sessionTTL        = 30 * 24 * time.Hour
	oidcTTL           = 10 * time.Minute
	devIssuer         = "dev"
)

var errBadCookie = errors.New("bad cookie")

// oidcPayload is the short-lived login state: OAuth state, OIDC nonce and the PKCE verifier.
type oidcPayload struct {
	StepUp   *stepupLogin `json:"stepup,omitempty"`
	State    string       `json:"s"`
	Nonce    string       `json:"n"`
	Verifier string       `json:"v"`
	Tenant   string       `json:"t"`
	Invite   string       `json:"i,omitempty"`
	Exp      int64        `json:"e"`
}

func (m *Module) secureCookies() bool { return !m.cfg.Dev() }

func (m *Module) seal(p oidcPayload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, m.cfg.SessionKey)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (m *Module) open(raw string) (oidcPayload, error) {
	bodyB64, sigB64, ok := strings.Cut(raw, ".")
	if !ok {
		return oidcPayload{}, errBadCookie
	}
	body, err := base64.RawURLEncoding.DecodeString(bodyB64)
	if err != nil {
		return oidcPayload{}, errBadCookie
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return oidcPayload{}, errBadCookie
	}
	mac := hmac.New(sha256.New, m.cfg.SessionKey)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return oidcPayload{}, errBadCookie
	}
	var p oidcPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return oidcPayload{}, errBadCookie
	}
	if p.State == "" || p.Nonce == "" || p.Verifier == "" || p.Tenant == "" || !time.Now().Before(time.Unix(p.Exp, 0)) {
		return oidcPayload{}, errBadCookie
	}
	return p, nil
}

func (m *Module) setOIDCCookie(w http.ResponseWriter, p oidcPayload) error {
	v, err := m.seal(p)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcCookieName,
		Value:    v,
		Path:     "/api/auth",
		MaxAge:   int(oidcTTL.Seconds()),
		HttpOnly: true,
		Secure:   m.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (m *Module) readOIDCCookie(r *http.Request) (oidcPayload, error) {
	c, err := r.Cookie(oidcCookieName)
	if err != nil {
		return oidcPayload{}, errBadCookie
	}
	return m.open(c.Value)
}

func (m *Module) clearOIDCCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcCookieName,
		Value:    "",
		Path:     "/api/auth",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   m.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *Module) setSessionCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		Expires:  time.Now().Add(sessionTTL),
		HttpOnly: true,
		Secure:   m.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *Module) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   m.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sessionID(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
