// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOfferRateLimiterBoundsAndWindow(t *testing.T) {
	l := &offerRateLimiter{}
	now := time.Now()
	if !l.allow("one", 1, now) || l.allow("one", 1, now) || !l.allow("one", 1, now.Add(time.Minute)) {
		t.Fatal("limit/window broken")
	}
	l.entries = make(map[string]offerLimitEntry, 10000)
	for i := 0; i < 10000; i++ {
		l.entries[string(rune(i))] = offerLimitEntry{until: now.Add(time.Minute)}
	}
	if l.allow("new-key", 1, now) {
		t.Fatal("unbounded limiter state")
	}
	if !l.allow("new-key", 1, now.Add(2*time.Minute)) {
		t.Fatal("expired entries not pruned")
	}
}
func TestOfferCapabilitySkipsOrdinaryLogger(t *testing.T) {
	old := ordinaryRequestLogger
	defer func() { ordinaryRequestLogger = old }()
	called := false
	ordinaryRequestLogger = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; next.ServeHTTP(w, r) })
	}
	h := ControlAwareRequestLogger(OfferPrivacyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })))
	for _, path := range []string{"/offers/malformed", "/api/public/offers/malformed/accept", "/api/public/offers/malformed/unknown"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if called {
			t.Fatal("capability reached ordinary URL logger")
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing privacy policy")
		}
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/customers", nil))
	if !called {
		t.Fatal("ordinary logging suppressed for unrelated routes")
	}
}

func TestOfferClientIPOnlyTrustsConfiguredProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.66")
	r.Header.Set("X-Real-IP", "198.51.100.77")
	t.Setenv("OFFER_TRUSTED_PROXY_CIDRS", "")
	if got := offerClientIP(r); got != "192.0.2.1" {
		t.Fatalf("trusted an unsolicited header: %s", got)
	}
	t.Setenv("OFFER_TRUSTED_PROXY_CIDRS", "172.17.0.1/32")
	if got := offerClientIP(r); got != "192.0.2.1" {
		t.Fatal("untrusted peer bypassed allowlist")
	}
	r.RemoteAddr = "172.17.0.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.66, 203.0.113.42")
	if got := offerClientIP(r); got != "203.0.113.42" {
		t.Fatalf("did not select nearest untrusted hop: %s", got)
	}
	r.Header.Set("X-Forwarded-For", "garbage")
	if got := offerClientIP(r); got != "172.17.0.1" {
		t.Fatal("malformed address trusted")
	}
}
func TestOfferDateValidation(t *testing.T) {
	for _, day := range []string{"10.10.2099", "2026-02-30", "2026-9-5", "", "2026-09-10T00:00:00Z"} {
		if offerDateValid(day) {
			t.Fatalf("invalid ISO deadline accepted: %s", day)
		}
	}
	if !offerDateValid("2028-02-29") {
		t.Fatal("valid leap date rejected")
	}
}
