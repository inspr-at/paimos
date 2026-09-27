// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountMetadataPublishesOnlyExplicitProjection(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "PUT" || r.URL.Path != "/api/agent-accounts/account/metadata" {
			t.Error("wrong metadata endpoint")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for key := range body {
			switch key {
			case "label", "plan", "host_label", "allowed_model_profile_ids":
			default:
				t.Errorf("unexpected metadata key: %s", key)
			}
		}
		if len(body) != 4 || string(body["allowed_model_profile_ids"]) != "[]" {
			t.Error("metadata grants missing or null")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	r := NewRemote(server.URL, "synthetic-scoped-key")
	metadata := AccountMetadata{Label: "Work account", Plan: "Pro", HostLabel: "Studio", AllowedProfileIDs: []string{}}
	if err := r.PublishAccountMetadata(t.Context(), "account", metadata); err != nil {
		t.Fatal(err)
	}
	metadata.AllowedProfileIDs = nil
	if err := r.PublishAccountMetadata(t.Context(), "account", metadata); err == nil {
		t.Fatal("missing grants accepted")
	}
	metadata.AllowedProfileIDs = []string{}
	metadata.Label = "unsafe\nlabel"
	if err := r.PublishAccountMetadata(t.Context(), "account", metadata); err == nil {
		t.Fatal("multiline metadata accepted")
	}
	if requests != 1 {
		t.Fatal("invalid metadata left the daemon")
	}
	metadata.Label, metadata.Plan, metadata.HostLabel = strings.Repeat("界", 128), strings.Repeat("界", 128), strings.Repeat("界", 128)
	if err := r.PublishAccountMetadata(t.Context(), "account", metadata); err != nil {
		t.Fatal("full-length Unicode metadata was rejected")
	}
	metadata.Label += "x"
	if err := r.PublishAccountMetadata(t.Context(), "account", metadata); err == nil || requests != 2 {
		t.Fatal("overlong metadata reached the server")
	}
}

func (a *fakeAPI) PublishAccountMetadata(_ context.Context, id string, metadata AccountMetadata) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.metadataReports = append(a.metadataReports, metadata)
	return nil
}

func TestSupervisorPublishesAccountMetadataOnce(t *testing.T) {
	metadata := &AccountMetadata{Label: "Work", Plan: "Pro", AllowedProfileIDs: []string{"profile"}}
	s, api, _ := testSupervisor(t, metadata)
	defer s.Close(context.Background())
	for range 2 {
		if err := s.PollOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.metadataReports) != 1 || api.metadataReports[0].Label != "Work" || api.metadataReports[0].AllowedProfileIDs[0] != "profile" {
		t.Fatal("metadata was not published exactly once at startup")
	}
}
