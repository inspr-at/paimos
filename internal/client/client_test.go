// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/stepup"
)

func TestStatusErrorRetainsBoundedServerRetryDelay(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"59", 59 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"86401", 0},
		{strings.Repeat("9", 129), 0},
		{"invalid", 0},
	} {
		t.Run(tc.header[:min(16, len(tc.header))], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"computer recovery attempt cap reached","code":"attach_recovery_limited"}`))
			}))
			defer srv.Close()
			err := New(srv.URL, "fixture").Do(t.Context(), "POST", "/api/agent-pairing/attach", nil, nil)
			status, ok := err.(*StatusError)
			if !ok || status.Status != 429 || status.RetryAfter != tc.want || status.Message != "computer recovery attempt cap reached" {
				t.Fatal("response lost its bounded retry instruction or original refusal")
			}
		})
	}
}

func TestRetryAfterHTTPDateUsesInjectedTime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, delay := range []time.Duration{-time.Second, 0, time.Minute, 24 * time.Hour, 25 * time.Hour} {
		want := delay
		if delay <= 0 || delay > 24*time.Hour {
			want = 0
		}
		if got := retryAfter(now.Add(delay).Format(http.TimeFormat), now); got != want {
			t.Fatal("HTTP date retry delay ignored time or its bound")
		}
	}
}

func TestMe(t *testing.T) {
	const token = "aeon_prefix_secretvalue"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("authorization header mismatch")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Me{
			Principal: Principal{ID: "p1", TenantID: "t1", Kind: "agent", Name: "cursor-grok", Roles: []string{"admin"}},
			Tenant:    Tenant{ID: "t1", Slug: "aeon", Name: "Aeon"},
		})
	}))
	defer srv.Close()

	me, err := New(srv.URL, token).Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.Principal.Name != "cursor-grok" || me.Principal.Kind != "agent" || me.Tenant.Slug != "aeon" {
		t.Fatalf("me = %+v", me)
	}
	if me.Identity != nil {
		t.Fatal("expected null identity")
	}
}

func TestMeUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "aeon_prefix_secretvalue").Me(context.Background())
	se, ok := err.(*StatusError)
	if !ok || se.Status != http.StatusUnauthorized || se.Message != "unauthorized" {
		t.Fatalf("err = %#v", err)
	}
	if strings.Contains(err.Error(), "secretvalue") {
		t.Fatal("error included the token")
	}
}

func TestStatusErrorRetainsAttachCauseWithoutChangingMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"project, ticket or harness enrollment changed","code":"conflict","attach_refusal":"ticket_not_visible"}`))
	}))
	defer srv.Close()
	err := New(srv.URL, "fixture").Do(t.Context(), "POST", "/api/agent-pairing/attach", nil, nil)
	se, ok := err.(*StatusError)
	if !ok || se.Status != 409 || se.Message != "project, ticket or harness enrollment changed" || se.AttachRefusal != "ticket_not_visible" {
		t.Fatal("additive cause or existing message lost")
	}
}

func TestStatusErrorRetainsPermissionReasonWithoutChangingMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"permission denied","code":"forbidden","reason_code":"missing_key_scope"}`))
	}))
	defer srv.Close()
	err := New(srv.URL, "fixture").Do(t.Context(), "POST", "/api/agent-pairing/attach", nil, nil)
	se, ok := err.(*StatusError)
	if !ok || se.Status != 403 || se.Message != "permission denied" || se.ReasonCode != "missing_key_scope" || se.AttachRefusal != "" {
		t.Fatal("additive permission reason or existing message lost")
	}
}

func TestDoPostsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["title"] != "hello" {
			t.Errorf("body %+v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer srv.Close()

	var out struct {
		ID string `json:"id"`
	}
	err := New(srv.URL+"/", "k").Do(context.Background(), http.MethodPost, "/api/items", map[string]string{"title": "hello"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != "1" {
		t.Fatalf("id %q", out.ID)
	}
}

// A list page of imported tickets passes 1 MiB; it decodes whole, and a body past
// the cap says so instead of failing as truncated JSON (AEON-140).
func TestDoReadsLargeResponsesAndNamesTheCap(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/huge" {
			_, _ = w.Write([]byte(`"` + strings.Repeat("y", MaxResponseBytes) + `"`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"body": big})
	}))
	defer srv.Close()
	var out map[string]string
	if err := New(srv.URL, "t").Do(context.Background(), http.MethodGet, "/api/nodes", nil, &out); err != nil || len(out["body"]) != len(big) {
		t.Fatalf("3 MiB response: err=%v len=%d", err, len(out["body"]))
	}
	var s string
	err := New(srv.URL, "t").Do(context.Background(), http.MethodGet, "/api/huge", nil, &s)
	if err == nil || !strings.Contains(err.Error(), "larger than 64 MiB") {
		t.Fatalf("oversized response: %v", err)
	}
}

func TestErrorMessagesReadBothErrorShapes(t *testing.T) {
	for payload, want := range map[string]string{
		`{"error":"node not found"}`:                        "node not found",
		`{"code":"forbidden","message":"not your session"}`: "not your session",
		`plain text`: "plain text",
	} {
		if got := errorMessage([]byte(payload)); got != want {
			t.Fatalf("%s: got %q, want %q", payload, got, want)
		}
	}
}

type workRetryTransport func(*http.Request) (*http.Response, error)

func (f workRetryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type workRetryBody struct {
	io.Reader
	closed bool
}

func (b *workRetryBody) Close() error { b.closed = true; return nil }

// Risk: retrying a write can duplicate an effect, while an exhausted read must
// retain its explicit failure. An injected wait proves bounds and cancellation.
func TestWorkAggregateReadRetryIsOptInOnceAndCancellable(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, message                     string
		enabled, success, cancel, proof, transportError bool
		body                                            any
		status, calls, waits                            int
	}{
		{name: "default off", method: "GET", path: "/api/nodes", status: 503, calls: 1},
		{name: "list success", method: "GET", path: "/api/nodes?cursor=stable&limit=20", enabled: true, success: true, status: 503, calls: 2, waits: 1},
		{name: "read exhaustion", method: "GET", path: "/api/nodes", enabled: true, status: 503, calls: 2, waits: 1},
		{name: "node read", method: "GET", path: "/api/nodes/6cf89e01-e5ea-4585-82fb-f5727c11fdd2", enabled: true, success: true, status: 503, calls: 2, waits: 1},
		{name: "key read", method: "GET", path: "/api/node-keys/AEON-1", enabled: true, success: true, status: 503, calls: 2, waits: 1},
		{name: "post", method: "POST", path: "/api/nodes", enabled: true, body: map[string]string{"title": "work"}, status: 503, calls: 1},
		{name: "empty post", method: "POST", path: "/api/nodes", enabled: true, status: 503, calls: 1},
		{name: "patch", method: "PATCH", path: "/api/nodes/6cf89e01-e5ea-4585-82fb-f5727c11fdd2", enabled: true, body: map[string]string{"title": "work"}, status: 503, calls: 1},
		{name: "put", method: "PUT", path: "/api/nodes/6cf89e01-e5ea-4585-82fb-f5727c11fdd2", enabled: true, status: 503, calls: 1},
		{name: "delete", method: "DELETE", path: "/api/nodes/6cf89e01-e5ea-4585-82fb-f5727c11fdd2", enabled: true, status: 503, calls: 1},
		{name: "other refusal", method: "GET", path: "/api/nodes", message: "unavailable", enabled: true, status: 503, calls: 1},
		{name: "other status", method: "GET", path: "/api/nodes", enabled: true, status: 429, calls: 1},
		{name: "other route", method: "GET", path: "/api/harness-sessions", enabled: true, status: 503, calls: 1},
		{name: "reserved node route", method: "GET", path: "/api/nodes/tree", enabled: true, status: 503, calls: 1},
		{name: "transport error", method: "GET", path: "/api/nodes", enabled: true, transportError: true, calls: 1},
		{name: "read body", method: "GET", path: "/api/nodes", enabled: true, body: map[string]string{"title": "work"}, status: 503, calls: 1},
		{name: "single use proof", method: "GET", path: "/api/nodes", enabled: true, proof: true, status: 503, calls: 1},
		{name: "cancelled backoff", method: "GET", path: "/api/nodes", enabled: true, cancel: true, status: 503, calls: 1, waits: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.enabled {
				ctx = WithWorkReadRetry(ctx)
			}
			message := tc.message
			if message == "" {
				message = workAggregateLimitMessage
			}
			payload, _ := json.Marshal(map[string]string{"error": message})
			calls, waits := 0, 0
			transportErr := errors.New("fixture transport refused")
			var bodies []*workRetryBody
			c := New("https://fixture.invalid", "fixture")
			c.HTTP.Transport = workRetryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != tc.method || r.URL.RequestURI() != tc.path || r.Header.Get("X-Fixture-Fence") != "owned" {
					t.Fatal("retry changed request identity or fencing headers")
				}
				if tc.transportError {
					return nil, transportErr
				}
				status, raw := tc.status, string(payload)
				if calls > 1 && tc.success {
					status, raw = 200, `{"value":"complete"}`
				}
				body := &workRetryBody{Reader: strings.NewReader(raw)}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: status, Body: body, Request: r, Header: http.Header{"Retry-After": {"86400"}}}, nil
			})
			c.workReadWait = func(waitCtx context.Context, delay time.Duration) error {
				waits++
				if delay != time.Second || !bodies[len(bodies)-1].closed {
					t.Fatal("backoff was unbounded or response remained open")
				}
				if tc.cancel {
					cancel()
				}
				return waitCtx.Err()
			}
			var out map[string]string
			headers := map[string]string{"X-Fixture-Fence": "owned"}
			if tc.proof {
				headers[stepup.Header] = "fixture-proof"
			}
			err := c.DoWithHeaders(ctx, tc.method, tc.path, tc.body, &out, headers)
			if calls != tc.calls || waits != tc.waits {
				t.Fatalf("calls=%d waits=%d", calls, waits)
			}
			if tc.transportError {
				if !errors.Is(err, transportErr) || out != nil {
					t.Fatalf("transport failure lost: %v", err)
				}
			} else if tc.cancel {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			} else if tc.success {
				if err != nil || out["value"] != "complete" {
					t.Fatalf("read: %v %+v", err, out)
				}
			} else {
				var status *StatusError
				if !errors.As(err, &status) || status.Status != tc.status || status.Message != message || out != nil {
					t.Fatalf("failure lost or reported as partial success: %v %+v", err, out)
				}
			}
			for _, body := range bodies {
				if !body.closed {
					t.Fatal("response was not closed")
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitWorkRead(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("real backoff did not observe cancellation: %v", err)
	}
}
