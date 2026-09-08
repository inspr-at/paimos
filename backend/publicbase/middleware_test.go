// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package publicbase

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRejectOutsideAndStrip(t *testing.T) {
	prefix, err := Parse("/paimos")
	if err != nil {
		t.Fatal(err)
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path+"|"+r.URL.RawPath)
	})
	handler := RejectOutsideAndStrip(prefix)(inner)

	t.Run("strips matching prefix", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/paimos/api/health", nil)
		req.Header.Set("X-Forwarded-Prefix", "/forged")
		req.Header.Set("X-Forwarded-User", "attacker")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		if rec.Body.String() != "/api/health|" {
			t.Fatalf("body=%q", rec.Body.String())
		}
	})

	t.Run("strips encoded opaque parameter after prefix", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/paimos/api/control-commands/a%2Fb", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		if rec.Body.String() != "/api/control-commands/a/b|/api/control-commands/a%2Fb" {
			t.Fatalf("body=%q", rec.Body.String())
		}
	})

	t.Run("404 outside mount including root api", func(t *testing.T) {
		for _, path := range []string{"/", "/api/health", "/pharos", "/paimos2/api/health", "/paimosfoo"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s status=%d want 404", path, rec.Code)
			}
			if rec.Body.String() == "/api/health|" {
				t.Errorf("%s reached inner handler", path)
			}
		}
	})
}

func TestRejectOutsideAndStripEmptyIsNoop(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	})
	handler := RejectOutsideAndStrip("")(inner)
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "/api/health" {
		t.Fatalf("empty prefix broke standalone: %d %q", rec.Code, rec.Body.String())
	}
}
