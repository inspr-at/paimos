// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestHTTPAuthorityBindsPrivateProofAndExactRuntime(t *testing.T) {
	lease, _ := NewProof()
	worker, _ := NewProof()
	id := uuid.NewString()
	generation := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get(lifecycleintents.RuntimeLeaseHeader) != lease {
			t.Error("authority headers missing")
		}
		if strings.HasSuffix(r.URL.Path, "/sessions") {
			if r.Header.Get(lifecycleintents.HarnessLeaseHeader) != worker {
				t.Error("worker proof missing")
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"registered": true})
			return
		}
		var in lifecycleintents.Registration
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Error("registration body")
		}
		_ = json.NewEncoder(w).Encode(lifecycleintents.Runtime{ID: id, ProjectID: 42, Generation: in.Generation, MachineID: in.Host, AccountLabel: in.AccountLabel, Workspaces: in.Workspaces, Profiles: in.Profiles, ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)})
	}))
	defer server.Close()
	h, e := NewHTTP(server.URL, 42, lease, func() (string, error) { return "fixture-key", nil })
	if e != nil {
		t.Fatal(e)
	}
	r, e := h.RegisterRuntime(context.Background(), lifecycleintents.Registration{Generation: generation, Host: "fixture-host", AccountLabel: "chatgpt", Workspaces: []lifecycleintents.Workspace{}, Profiles: []lifecycleintents.Profile{}})
	if e != nil || r.ID != id {
		t.Fatal(e)
	}
	if e = h.RegisterSession(context.Background(), id, lifecycleintents.SessionRegistration{SessionID: uuid.NewString(), Generation: uuid.NewString()}, worker); e != nil {
		t.Fatal(e)
	}
	if e = h.Request(context.Background(), http.MethodGet, "/api/projects/99/lifecycle/v1/runtimes", nil, nil, &r); e == nil {
		t.Fatal("cross-project request escaped")
	}
}
func TestHTTPDoesNotRedirectOrEchoUntrustedError(t *testing.T) {
	visited := false
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { visited = true }))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/credential-sink", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	lease, _ := NewProof()
	h, _ := NewHTTP(server.URL, 42, lease, func() (string, error) { return "fixture-key", nil })
	err := h.Request(context.Background(), http.MethodPost, "/api/projects/42/lifecycle/v1/runtimes", nil, struct{}{}, nil)
	if err == nil || visited || strings.Contains(err.Error(), "fixture-key") || strings.Contains(err.Error(), foreign.URL) {
		t.Fatal("redirect or diagnostic leaked authority")
	}
}
func TestPrivateCredentialRejectsSymlinksHardlinksAndPublicMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential")
	if e := os.WriteFile(path, []byte("fixture-key\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if value, e := FileCredential(path)(); e != nil || value != "fixture-key" {
		t.Fatal("safe credential refused")
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivate(link, 4096); e == nil {
		t.Fatal("symlink accepted")
	}
	hard := filepath.Join(dir, "hard")
	if e := os.Link(path, hard); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivate(path, 4096); e == nil {
		t.Fatal("hardlink accepted")
	}
	public := filepath.Join(dir, "public")
	if e := os.WriteFile(public, []byte("fixture-key"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivate(public, 4096); e == nil {
		t.Fatal("public credential accepted")
	}
}

func TestHTTPConsumerConflictUsesOnlyBoundedClosedErrorVocabulary(t *testing.T) {
	for _, fixture := range []struct {
		code     string
		expected error
	}{{"consumer_outcome_unknown", ErrUnknown}, {"consumer_handoff_required", ErrHandoff}, {"private-untrusted-error", lifecycleintents.ErrConflict}} {
		t.Run(fixture.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(409)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": fixture.code})
			}))
			defer srv.Close()
			lease, _ := NewProof()
			h, _ := NewHTTP(srv.URL, 42, lease, func() (string, error) { return "fixture", nil })
			if e := h.Request(context.Background(), http.MethodPost, "/api/projects/42/consumers/v1/streams", nil, struct{}{}, nil); e != fixture.expected {
				t.Fatal("closed consumer error lost", e)
			}
			if e := h.Request(context.Background(), http.MethodPost, "/api/projects/42/lifecycle/v1/runtimes", nil, struct{}{}, nil); e != lifecycleintents.ErrConflict {
				t.Fatal("consumer error vocabulary escaped consumer routes")
			}
		})
	}
}
