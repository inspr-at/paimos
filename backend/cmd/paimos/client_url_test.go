// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type clientURLRoundTripFunc func(*http.Request) (*http.Response, error)

func (f clientURLRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestJoinInstanceAPIOnce(t *testing.T) {
	cases := []struct {
		base string
		path string
		want string
	}{
		{"https://pm.example.com", "/api/health", "https://pm.example.com/api/health"},
		{"https://pm.example.com/", "/api/health", "https://pm.example.com/api/health"},
		{"https://pm.example.com/paimos", "/api/health", "https://pm.example.com/paimos/api/health"},
		{"https://pm.example.com/paimos/", "/api/health", "https://pm.example.com/paimos/api/health"},
		{"https://pm.example.com/apps/paimos", "/api/projects/7/agents", "https://pm.example.com/apps/paimos/api/projects/7/agents"},
	}
	for _, tc := range cases {
		got := joinInstanceAPI(tc.base, tc.path)
		if got != tc.want {
			t.Errorf("join(%q,%q)=%q want %q", tc.base, tc.path, got, tc.want)
		}
		if count := countAPISeg(got); count != 1 {
			t.Errorf("join(%q,%q) contains %d /api segments", tc.base, tc.path, count)
		}
	}
}

func TestPrefixedClientMessagingReadinessSeam(t *testing.T) {
	oldAgent := flagAgentName
	flagAgentName = "codex"
	t.Cleanup(func() { flagAgentName = oldAgent })

	seen := make([]string, 0, 3)
	client := &Client{
		baseURL: "https://pm.example.test/paimos",
		http: &http.Client{Transport: clientURLRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet {
				t.Fatalf("readiness probe mutated state with %s", r.Method)
			}
			if !strings.HasPrefix(r.URL.Path, "/paimos/api/") {
				t.Fatalf("readiness request escaped public prefix: %s", r.URL.Path)
			}
			seen = append(seen, r.URL.RequestURI())
			body := ""
			switch r.URL.Path {
			case "/paimos/api/projects/42/agents":
				body = `[{"id":7,"project_id":42,"name":"codex"}]`
			case "/paimos/api/projects/42/message-allowlist":
				if r.URL.Query().Get("receiver") != "cursor:worker" {
					t.Fatalf("allowlist receiver=%q", r.URL.Query().Get("receiver"))
				}
				body = `{"receiver":"cursor:worker","senders":["paimos:codex"]}`
			case "/paimos/api/projects/42/message-targets":
				if r.URL.Query().Get("address") != "cursor:worker" {
					t.Fatalf("target address=%q", r.URL.Query().Get("address"))
				}
				body = `{"targets":[]}`
			default:
				t.Fatalf("unexpected readiness route: %s", r.URL.RequestURI())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		})},
	}

	readiness := probeFriendlyMessaging(context.Background(), client, 42, "cursor:worker", nil, false)
	if readiness.Sender.State != friendlyReady || readiness.Policy.State != friendlyReady || readiness.Fallback.State != friendlyMissing {
		t.Fatalf("readiness=%+v", readiness)
	}
	if len(seen) != 3 {
		t.Fatalf("readiness requests=%v", seen)
	}
}

func countAPISeg(u string) int {
	n, from := 0, 0
	for {
		i := indexAPI(u[from:])
		if i < 0 {
			return n
		}
		n++
		from += i + 4
	}
}

func indexAPI(s string) int {
	for i := 0; i+4 <= len(s); i++ {
		if s[i:i+4] == "/api" && (i+4 == len(s) || s[i+4] == '/' || s[i+4] == '?') {
			return i
		}
	}
	return -1
}
