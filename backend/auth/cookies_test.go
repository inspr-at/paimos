// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/publicbase"
)

func TestStandaloneDualWriteAndDualRead(t *testing.T) {
	publicbase.SetCurrent("")
	t.Cleanup(func() { publicbase.SetCurrent("") })

	rec := httptest.NewRecorder()
	setSessionCookieValue(rec, "sid-standalone", time.Now().Add(time.Hour))
	SetCSRFCookie(rec, "csrf-standalone")

	var sawSession, sawLegacySession, sawCSRF, sawLegacyCSRF bool
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case sessionCookie:
			sawSession = true
			if c.Path != "/" || !c.HttpOnly {
				t.Fatalf("namespaced session attrs: %+v", c)
			}
		case legacySessionCookie:
			sawLegacySession = true
		case CSRFCookieName:
			sawCSRF = true
			if c.HttpOnly {
				t.Fatal("CSRF cookie must stay readable by the SPA")
			}
		case legacyCSRFCookieName:
			sawLegacyCSRF = true
		}
	}
	if !sawSession || !sawLegacySession || !sawCSRF || !sawLegacyCSRF {
		t.Fatalf("standalone must dual-write; cookies=%v", rec.Result().Cookies())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: legacySessionCookie, Value: "legacy-sid"})
	got, err := readSessionCookie(req)
	if err != nil || got.Value != "legacy-sid" {
		t.Fatalf("standalone dual-read failed: %v %#v", err, got)
	}
}

func TestSharedOriginRejectsGenericCookiesAndDoesNotWriteThem(t *testing.T) {
	prefix, err := publicbase.Parse("/paimos")
	if err != nil {
		t.Fatal(err)
	}
	publicbase.SetCurrent(prefix)
	t.Cleanup(func() { publicbase.SetCurrent("") })

	rec := httptest.NewRecorder()
	setSessionCookieValue(rec, "sid-shared", time.Now().Add(time.Hour))
	SetCSRFCookie(rec, "csrf-shared")
	setShortCookie(rec, oidcStateCookie, legacyOIDCStateCookie, "state")

	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case legacySessionCookie, legacyCSRFCookieName, legacyOIDCStateCookie:
			t.Fatalf("shared-origin wrote generic cookie %q", c.Name)
		}
		if c.Path != "/" {
			t.Fatalf("cookie %q Path=%q; Path is not a security boundary and must stay /", c.Name, c.Path)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/paimos/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: legacySessionCookie, Value: "stolen-generic"})
	req.AddCookie(&http.Cookie{Name: legacyCSRFCookieName, Value: "stolen-csrf"})
	req.AddCookie(&http.Cookie{Name: legacyOIDCStateCookie, Value: "stolen-state"})
	if _, err := readSessionCookie(req); err == nil {
		t.Fatal("shared-origin accepted generic session cookie")
	}
	if _, err := readCSRFCookie(req); err == nil {
		t.Fatal("shared-origin accepted generic CSRF cookie")
	}
	if _, err := readOIDCCookie(req, oidcStateCookie, legacyOIDCStateCookie); err == nil {
		t.Fatal("shared-origin accepted generic OIDC cookie")
	}

	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "ok-sid"})
	got, err := readSessionCookie(req)
	if err != nil || got.Value != "ok-sid" {
		t.Fatalf("namespaced session should win: %v %#v", err, got)
	}
}

func TestSharedOriginLogoutDoesNotExpireGenericCookies(t *testing.T) {
	prefix, _ := publicbase.Parse("/paimos")
	publicbase.SetCurrent(prefix)
	t.Cleanup(func() { publicbase.SetCurrent("") })

	rec := httptest.NewRecorder()
	clearSessionCookies(rec)
	clearOIDCCookies(rec)
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case legacySessionCookie, legacyCSRFCookieName, legacyOIDCStateCookie, legacyOIDCVerifCookie, legacyOIDCNonceCookie:
			t.Fatalf("shared-origin logout expired generic cookie %q", c.Name)
		}
	}
}

func TestSafeOIDCReturnPathPreservesQuery(t *testing.T) {
	prefix, _ := publicbase.Parse("/paimos")
	publicbase.SetCurrent(prefix)
	t.Cleanup(func() { publicbase.SetCurrent("") })

	got := safeOIDCReturnPath("/projects/6?tab=overview#baseline-batch")
	if got != "/paimos/projects/6?tab=overview#baseline-batch" {
		t.Fatalf("got %q", got)
	}
	if safeOIDCReturnPath("//evil.example") != "" {
		t.Fatal("protocol-relative return must be rejected")
	}
	if safeOIDCReturnPath("/login") != "" {
		t.Fatal("login loop must be rejected")
	}
}

func TestBrowserPathJoinsPrefix(t *testing.T) {
	publicbase.SetCurrent("")
	if got := browserPath("/login"); got != "/login" {
		t.Fatalf("standalone login = %q", got)
	}
	prefix, _ := publicbase.Parse("/paimos")
	publicbase.SetCurrent(prefix)
	t.Cleanup(func() { publicbase.SetCurrent("") })
	if got := browserPath("/login"); got != "/paimos/login" {
		t.Fatalf("prefixed login = %q", got)
	}
	if got := browserPath("https://idp.example/end"); got != "https://idp.example/end" {
		t.Fatalf("absolute URL rewritten: %q", got)
	}
}

func TestValidateOIDCRedirectURLMatchesPrefix(t *testing.T) {
	publicbase.SetCurrent("")
	if err := validateOIDCRedirectURL("https://paimos.example.test/api/auth/oidc/callback"); err != nil {
		t.Fatalf("standalone callback: %v", err)
	}
	prefix, _ := publicbase.Parse("/paimos")
	publicbase.SetCurrent(prefix)
	t.Cleanup(func() { publicbase.SetCurrent("") })
	if err := validateOIDCRedirectURL("https://example.test/paimos/api/auth/oidc/callback"); err != nil {
		t.Fatalf("prefixed callback: %v", err)
	}
	if err := validateOIDCRedirectURL("https://example.test/api/auth/oidc/callback"); err == nil {
		t.Fatal("root callback must be rejected when prefix is set")
	}
}
