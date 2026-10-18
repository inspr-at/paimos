// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryInstallationUsesOnlyGrantedReadPermissions(t *testing.T) {
	// Risk: an App without Actions cannot mint a token, or optional grants widen
	// authority. Validate the actual request and refusal/cleanup boundaries.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "fixture.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, actions, source            string
		badToken, badRepo, missingChecks bool
	}{
		{name: "least privilege", source: "checks"},
		{name: "Actions read", actions: "read", source: "actions"},
		{name: "Actions write downscoped", actions: "write", source: "actions"},
		{name: "unexpected token write", source: "checks", badToken: true},
		{name: "another repository", source: "checks", badRepo: true},
		{name: "missing checks grant", source: "checks", missingChecks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			minted, revoked, reads, callbacks := 0, 0, 0, 0
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				var body any
				switch r.Method + " " + r.URL.Path {
				case "GET /app/installations/456":
					grants := map[string]string{"checks": "read", "contents": "read", "pull_requests": "read", "metadata": "read"}
					if tc.actions != "" {
						grants["actions"] = tc.actions
					}
					if tc.missingChecks {
						delete(grants, "checks")
					}
					body = map[string]any{"id": 456, "app_id": 123, "permissions": grants}
				case "POST /app/installations/456/access_tokens":
					var in struct {
						Permissions  map[string]string `json:"permissions"`
						Repositories []string          `json:"repositories"`
					}
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Fatal(err)
					}
					want := 4
					if tc.actions != "" {
						want++
					}
					if len(in.Permissions) != want || len(in.Repositories) != 1 || in.Repositories[0] != "delivery" {
						t.Fatal("token authority drift")
					}
					for name, level := range in.Permissions {
						if level != "read" || !(name == "checks" || name == "contents" || name == "pull_requests" || name == "metadata" || name == "actions" && tc.actions != "") {
							t.Fatal("ungranted token permission")
						}
					}
					if tc.badToken {
						in.Permissions["contents"] = "write"
					}
					repo := "example/delivery"
					if tc.badRepo {
						repo = "example/other"
					}
					body = map[string]any{"token": "history-fixture-token", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": repo}}}
					minted++
				case "GET /repos/example/delivery/check-suites/7/check-runs", "GET /repos/example/delivery/actions/runs":
					if tc.source == "checks" && strings.Contains(r.URL.Path, "/actions/") {
						t.Fatal("ungranted Actions request reached GitHub")
					}
					body = map[string]any{}
					reads++
				case "DELETE /installation/token":
					body = map[string]any{}
					revoked++
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				raw, _ := json.Marshal(body)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			app := &GitHubApp{Config: AppConfig{ID: "123", InstallationID: "456", KeyFile: keyFile, TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Repository: "example/delivery"}, Client: client}
			err := app.ReadHistoryInstallation(t.Context(), func(get func(string, any) error, source string) error {
				callbacks++
				if source != tc.source {
					t.Fatal("wrong source")
				}
				if source == "checks" {
					if err := get("/actions/runs", &map[string]any{}); err == nil {
						t.Fatal("checks channel admitted Actions")
					}
					return get("/check-suites/7/check-runs", &map[string]any{})
				}
				return get("/actions/runs", &map[string]any{})
			})
			if tc.missingChecks {
				if err == nil || minted != 0 || callbacks != 0 {
					t.Fatal("missing grant did not refuse before minting")
				}
			} else if tc.badRepo || tc.badToken {
				if err == nil || callbacks != 0 || reads != 0 || revoked != 1 {
					t.Fatal("unsafe token admitted or not revoked")
				}
			} else if err != nil || callbacks != 1 || reads != 1 || minted != 1 || revoked != 1 {
				t.Fatalf("history read: %v callbacks=%d reads=%d minted=%d revoked=%d", err, callbacks, reads, minted, revoked)
			}
		})
	}
}
