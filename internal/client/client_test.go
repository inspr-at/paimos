// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
