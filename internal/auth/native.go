// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/devicesessions"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

var (
	nativeState    = regexp.MustCompile(`^[A-Za-z0-9_-]{22,128}$`)
	nativeVerifier = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	nativeSlug     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	nativeID       = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
)

// canonicalNativeOrigin accepts only an HTTPS origin, without an alias path,
// credentials, query or fragment. The configured origin is the trust anchor;
// forwarded headers never select an issuer or a tenant.
func canonicalNativeOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	if u.Port() == "443" {
		host = strings.ToLower(u.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return "https://" + host
}

func (m *Module) nativeOrigin(r *http.Request) string {
	origin := canonicalNativeOrigin(m.cfg.PublicURL)
	if origin == "" || canonicalNativeOrigin("https://"+r.Host) != origin {
		return ""
	}
	if values := r.Header.Values("Origin"); len(values) > 1 || len(values) == 1 && values[0] != origin {
		return ""
	}
	return origin
}

func (m *Module) handleNativeStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !m.nativeAttempt(w, r, "start", 20) {
		return
	}
	origin := m.nativeOrigin(r)
	if origin == "" || len(r.URL.RawQuery) > 2048 {
		httpapi.WriteError(w, 400, "native sign-in refused")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		httpapi.WriteError(w, 400, "invalid native sign-in parameters")
		return
	}
	for _, key := range []string{"tenant", "code_challenge", "code_challenge_method", "state", "redirect_uri"} {
		if len(q[key]) != 1 {
			httpapi.WriteError(w, 400, "invalid native sign-in parameters")
			return
		}
	}
	challenge, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if !nativeSlug.MatchString(q.Get("tenant")) || !nativeState.MatchString(q.Get("state")) ||
		q.Get("redirect_uri") != devicesessions.RedirectURI || q.Get("code_challenge_method") != "S256" ||
		err != nil || len(challenge) != 32 || base64.RawURLEncoding.EncodeToString(challenge) != q.Get("code_challenge") {
		httpapi.WriteError(w, 400, "invalid native sign-in parameters")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	tid, err := m.tenantBySlug(ctx, q.Get("tenant"))
	if err != nil {
		httpapi.WriteError(w, 400, "native sign-in refused")
		return
	}
	id, err := m.DeviceSessions.Start(ctx, devicesessions.Start{TenantID: tid, TenantSlug: q.Get("tenant"),
		Challenge: q.Get("code_challenge"), Origin: origin, State: q.Get("state")})
	if errors.Is(err, devicesessions.ErrRateLimited) {
		nativeRateLimited(w)
		return
	}
	if err != nil {
		internalFailure(w, r, "native_login_start", err)
		return
	}
	m.beginLogin(w, r, q.Get("tenant"), "", &nativeLogin{Tx: id})
}

func (m *Module) finishNativeLogin(w http.ResponseWriter, r *http.Request, payload oidcPayload, p tenant.Principal, identityID string) {
	origin := m.nativeOrigin(r)
	if origin == "" || !nativeID.MatchString(payload.Native.Tx) {
		m.callbackFailure(w, r, "failed", "native_callback_refused")
		return
	}
	out, err := m.DeviceSessions.Complete(r.Context(), devicesessions.Completion{TenantID: p.TenantID, TenantSlug: payload.Tenant,
		TransactionID: payload.Native.Tx, Origin: origin, PersonID: p.ID, IdentityID: identityID})
	if err != nil {
		m.callbackFailure(w, r, "failed", "native_callback_refused")
		return
	}
	// Complete returned after commit. Only now may the captured state leave
	// the server. Never mint, replace or clear a browser session on this path.
	m.clearOIDCCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	location := devicesessions.RedirectURI + "?" + url.Values{"code": {out.Code}, "state": {out.State}}.Encode()
	http.Redirect(w, r, location, http.StatusFound)
}

func (m *Module) handleNativeToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if !m.nativeAttempt(w, r, "token", 60) {
		return
	}
	origin := m.nativeOrigin(r)
	if origin == "" {
		httpapi.WriteError(w, 401, "native login code refused")
		return
	}
	if len(r.URL.RawQuery) > 256 {
		httpapi.WriteError(w, 400, "invalid exchange request")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["tenant"]) > 1 || len(q["tenant"]) == 1 && !nativeSlug.MatchString(q.Get("tenant")) {
		httpapi.WriteError(w, 400, "invalid exchange request")
		return
	}
	var in struct {
		Code        string `json:"code"`
		Verifier    string `json:"code_verifier"`
		State       string `json:"state"`
		RedirectURI string `json:"redirect_uri"`
		DeviceLabel string `json:"device_label"`
	}
	decoder := httpapi.NewRequestDecoder(w, r, 2048)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || len(in.Code) > 128 || in.Code == "" ||
		!nativeState.MatchString(in.State) || !nativeVerifier.MatchString(in.Verifier) ||
		!utf8.ValidString(in.DeviceLabel) || utf8.RuneCountInString(in.DeviceLabel) > 120 || strings.ContainsFunc(in.DeviceLabel, unicode.IsControl) {
		httpapi.WriteError(w, 400, "invalid exchange request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	out, err := m.DeviceSessions.Exchange(ctx, devicesessions.Exchange{Code: in.Code, Verifier: in.Verifier, State: in.State,
		RedirectURI: in.RedirectURI, Origin: origin, TenantSlug: q.Get("tenant"), DeviceLabel: strings.TrimSpace(in.DeviceLabel)})
	if errors.Is(err, devicesessions.ErrRefused) {
		httpapi.WriteError(w, 401, "native login code refused")
		return
	}
	if err != nil {
		internalFailure(w, r, "native_login_exchange", err)
		return
	}
	writeJSON(w, 200, out)
}

type nativeWindow struct {
	at    time.Time
	count int
}
type nativeLimits struct {
	mu    sync.Mutex
	peers map[string]nativeWindow
}

// Ignore attacker-controlled forwarding headers. Limit both work and memory
// before discovery, tenant lookups or token decoding; tenant starts also have
// a persistent limit under the tenant fence.
func (m *Module) nativeAttempt(w http.ResponseWriter, r *http.Request, operation string, limit int) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if !m.nativeLimits.allow(operation+":"+peer, time.Now(), limit) {
		nativeRateLimited(w)
		return false
	}
	return true
}

func (l *nativeLimits) allow(key string, now time.Time, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.peers == nil {
		l.peers = make(map[string]nativeWindow)
	}
	for key, window := range l.peers {
		if !now.Before(window.at.Add(time.Minute)) {
			delete(l.peers, key)
		}
	}
	window, ok := l.peers[key]
	if !ok {
		if len(l.peers) >= 4096 {
			return false
		}
		window.at = now
	}
	if window.count >= limit {
		return false
	}
	window.count++
	l.peers[key] = window
	return true
}

func nativeRateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	httpapi.WriteError(w, 429, "native sign-in attempt limit reached")
}
