// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZitadelCreateThenSendInvite(t *testing.T) {
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing bearer authorization")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		switch r.URL.Path {
		case "/v2/users":
			queries, ok := body["queries"].([]any)
			if !ok || len(queries) != 2 || body["query"] == nil {
				t.Error("missing bounded, scoped search")
			}
			_, _ = w.Write([]byte(`{"result":[]}`))
		case "/v2/users/new":
			if body["organizationId"] != "org-1" {
				t.Error("wrong organization")
			}
			human := body["human"].(map[string]any)
			email := human["email"].(map[string]any)
			if email["email"] != "nora@example.com" || email["isVerified"] != nil || email["sendCode"] != nil {
				t.Error("creation must leave verification to invite code")
			}
			profile := human["profile"].(map[string]any)
			if profile["givenName"] != "Nora" || profile["familyName"] != "Example" {
				t.Error("incorrect profile")
			}
			_, _ = w.Write([]byte(`{"id":"new-subject"}`))
		case "/v2/users/new-subject/invite_code":
			if _, ok := body["sendCode"]; !ok {
				t.Error("invite code must be emailed by Zitadel")
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result, err := newZitadel(server.URL, "org-1", "test-token", "tenant-1").EnsureUser(t.Context(), "nora@example.com", "Nora Example")
	if err != nil || result.Status != "invited" || result.Subject != "new-subject" {
		t.Fatalf("result: %+v, %v", result, err)
	}
	if strings.Join(paths, ",") != "/v2/users,/v2/users/new,/v2/users/new-subject/invite_code" {
		t.Fatalf("paths: %v", paths)
	}
}

func TestZitadelExistingUserIsUntouched(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v2/users" {
			t.Errorf("existing user mutated at %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"result":[{"userId":"existing","details":{"resourceOwner":"org-1"},"human":{"email":{"email":"nora@example.com"}}}]}`))
	}))
	defer server.Close()
	result, err := newZitadel(server.URL, "org-1", "test-token", "tenant-1").EnsureUser(t.Context(), "nora@example.com", "Nora Example")
	if err != nil || result.Status != "exists" || calls != 1 {
		t.Fatalf("result: %+v, %v, calls %d", result, err, calls)
	}
}

func TestZitadelFailedInviteCarriesOnlyCreatedSubjectForRetry(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users":
			_, _ = w.Write([]byte(`{"result":[]}`))
		case "/v2/users/new":
			_, _ = w.Write([]byte(`{"id":"created"}`))
		case "/v2/users/created/invite_code":
			sends++
			if sends == 1 {
				http.Error(w, `provider detail with test-token`, 503)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	provisioner := newZitadel(server.URL, "org-1", "test-token", "tenant-1")
	_, err := provisioner.EnsureUser(t.Context(), "nora@example.com", "Nora Example")
	var pe *ProvisionError
	if !strings.Contains(err.Error(), "identity provisioning failed") || !asProvisionError(err, &pe) || pe.Subject != "created" || strings.Contains(err.Error(), "test-token") {
		t.Fatalf("unsafe failure: %v", err)
	}
	if err := provisioner.(InviteSender).SendInvite(context.Background(), pe.Subject); err != nil || sends != 2 {
		t.Fatalf("retry: %v, sends %d", err, sends)
	}
}

func asProvisionError(err error, target **ProvisionError) bool {
	value, ok := err.(*ProvisionError)
	if ok {
		*target = value
	}
	return ok
}

func TestFromEnvDefaultAndCredentialFile(t *testing.T) {
	t.Setenv("AEON_IDENTITY_PROVISIONER", "")
	if p, err := FromEnv(); err != nil || p != nil {
		t.Fatalf("default: %v, %v", p, err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_IDENTITY_PROVISIONER", "zitadel")
	t.Setenv("AEON_IDENTITY_TENANT_ID", "tenant-1")
	t.Setenv("AEON_ZITADEL_URL", "https://zitadel.example")
	t.Setenv("AEON_ZITADEL_ORG_ID", "org-1")
	t.Setenv("AEON_ZITADEL_TOKEN_FILE", path)
	p, err := FromEnv()
	if err != nil || p == nil || p.Name() != "Zitadel" {
		t.Fatalf("configured: %v", err)
	}
	t.Setenv("AEON_ZITADEL_URL", "http://zitadel.example")
	if _, err := FromEnv(); err == nil {
		t.Fatal("insecure origin accepted")
	}
}
