// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCurlCommandUsesConfiguredAuthAndPrefixesAPI(t *testing.T) {
	var gotAuth, gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		if r.URL.Path != "/api/portal/overview" {
			t.Fatalf("path=%q, want /api/portal/overview", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "curl", "/portal/overview")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.TrimSpace(out) != `{"ok":true}` {
		t.Fatalf("stdout=%q", out)
	}
	if gotAuth != "Bearer test_key" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method=%q", gotMethod)
	}
	if gotBody != "" {
		t.Fatalf("body=%q, want empty", gotBody)
	}
}

func TestCurlCommandPostsInlineJSON(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		if r.Method != http.MethodPost {
			t.Fatalf("method=%q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type=%q", ct)
		}
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	if _, _, err := executeCLIForTest(t, "curl", "/api/test", "--method", "POST", "--data", `{"x":1}`); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotBody != `{"x":1}` {
		t.Fatalf("body=%q", gotBody)
	}
}

func TestCurlCommandReportsHTTPFailureInJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"forbidden","code":"forbidden"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	_, errOut, err := executeCLIForTest(t, "--json", "curl", "/api/secret")
	if err == nil {
		t.Fatal("HTTP failure unexpectedly succeeded")
	}
	if _, ok := err.(*apiError); !ok {
		t.Fatalf("error type=%T, want *apiError", err)
	}
	if !strings.Contains(errOut, `"code":403`) || !strings.Contains(errOut, "forbidden") {
		t.Fatalf("stderr=%q, want one machine-readable reported failure", errOut)
	}
}

func TestCurlCommandReportsTransportFailureInJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()
	t.Setenv(envURL, url)
	t.Setenv(envAPIKey, "test_key")

	_, errOut, err := executeCLIForTest(t, "--json", "curl", "/api/unavailable")
	if err == nil {
		t.Fatal("transport failure unexpectedly succeeded")
	}
	if _, ok := err.(*apiError); !ok {
		t.Fatalf("error type=%T, want *apiError", err)
	}
	if !strings.Contains(errOut, "HTTP GET") || strings.Count(errOut, `"error"`) != 1 {
		t.Fatalf("stderr=%q, want one reported transport failure", errOut)
	}
}
