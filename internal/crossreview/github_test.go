// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
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

	"github.com/inspr-at/paimos/internal/reviewgate"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGitHubStatusExactHeadAndNarrowAuthority(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("fixture signing key could not be generated")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "fixture-signing")
	if err = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, head      string
		base            string
		extraPermission bool
		want            string
		posts           int
	}{{"exact", strings.Repeat("b", 40), strings.Repeat("a", 40), false, "success", 1}, {"stale", strings.Repeat("c", 40), strings.Repeat("a", 40), false, "stale", 0}, {"excess authority", strings.Repeat("b", 40), strings.Repeat("a", 40), true, "error", 0}, {"wrong base", strings.Repeat("b", 40), strings.Repeat("c", 40), false, "stale", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			config := AppConfig{ID: "1", InstallationID: "2", KeyFile: file, TenantID: testID(), Repository: "example/review-fixture"}
			posts, revokes := 0, 0
			app := &GitHubApp{Config: config, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.github.com" || r.URL.Scheme != "https" {
					t.Fatal("unexpected host")
				}
				body := "{}"
				code := 200
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/2/access_tokens":
					var input struct {
						Repositories []string          `json:"repositories"`
						Permissions  map[string]string `json:"permissions"`
					}
					if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Repositories) != 1 || input.Repositories[0] != "review-fixture" || len(input.Permissions) != 2 || input.Permissions["statuses"] != "write" || input.Permissions["pull_requests"] != "read" {
						t.Fatal("installation authority was broadened")
					}
					permissions := map[string]string{"statuses": "write", "pull_requests": "read", "metadata": "read"}
					if tc.extraPermission {
						permissions["contents"] = "write"
					}
					raw, _ := json.Marshal(map[string]any{"token": "fixture-only-token", "permissions": permissions, "repositories": []map[string]string{{"full_name": config.Repository}}})
					body = string(raw)
				case "GET /repos/example/review-fixture/pulls/12":
					body = `{"number":12,"head":{"sha":"` + tc.head + `"},"base":{"sha":"` + tc.base + `","repo":{"full_name":"example/review-fixture"}}}`
				case "POST /repos/example/review-fixture/statuses/" + strings.Repeat("b", 40):
					posts++
					var input map[string]string
					if json.NewDecoder(r.Body).Decode(&input) != nil || input["state"] != map[bool]string{true: "error", false: "success"}[tc.name == "wrong base"] || input["context"] != "aeon/review" {
						t.Fatal("status not bound to the review")
					}
				case "DELETE /installation/token":
					revokes++
					code = 204
				default:
					t.Fatal("unexpected operation")
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
			})}}
			pr := int64(12)
			v := Review{GateOpen: true, Binding: reviewgate.Binding{Repository: config.Repository, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), PullRequest: &pr}}
			got, err := app.Publish(context.Background(), config.TenantID, v, "success")
			if got != tc.want || (err != nil) != (tc.want == "error") || posts != tc.posts || revokes != 1 {
				t.Fatal("exact-head or token-lifetime protection failed")
			}
			v.GateOpen = false
			if _, err := app.Publish(context.Background(), config.TenantID, v, "success"); err == nil {
				t.Fatal("closed gate posted approval")
			}
			v.GateOpen = true
			got, err = app.Publish(context.Background(), testID(), v, "success")
			if got != "error" || err == nil {
				t.Fatal("foreign tenant used App")
			}
		})
	}
}
func TestStatusStateRequiresAnOpenGate(t *testing.T) {
	model := "review-model"
	for _, tc := range []struct {
		status, verdict string
		open            bool
		want            string
	}{{"completed", "ok", false, "error"}, {"completed", "ok", true, "success"}, {"completed", "changes", false, "failure"}, {"running", "ok", false, "pending"}, {"blocked", "", false, "error"}, {"cancelled", "ok", false, "error"}} {
		v := Review{Status: tc.status, GateOpen: tc.open, Result: reviewgate.Result{Verdict: tc.verdict}, Model: &model, EffectiveModel: &model, modelEvidence: "vendor_reported"}
		if statusState(v) != tc.want {
			t.Fatal("closed gate published success")
		}
	}
}

func TestStatusStateRequiresVerifiedModelForChanges(t *testing.T) {
	model, empty, different := "review-model", "", "different-model"
	alias, concrete := "fable", "claude-fable-fixture"
	for _, tc := range []struct {
		name, evidence, want string
		model, effective     *string
	}{
		{"no evidence", "", "error", &model, &model},
		{"unverified", "unverified", "error", &model, &model},
		{"missing pin", "vendor_reported", "error", nil, &model},
		{"empty pin", "vendor_reported", "error", &empty, &model},
		{"missing model", "vendor_reported", "error", &model, nil},
		{"empty model", "vendor_reported", "error", &model, &empty},
		{"different model", "vendor_reported", "error", &model, &different},
		{"verified pinned model", "vendor_reported", "failure", &model, &model},
		{"verified alias", "vendor_reported", "failure", &alias, &concrete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := Review{Status: "completed", Result: reviewgate.Result{Verdict: "changes"}, Model: tc.model, EffectiveModel: tc.effective, modelEvidence: tc.evidence}
			if got := statusState(v); got != tc.want {
				t.Fatalf("completed changes status = %s, want %s", got, tc.want)
			}
		})
	}
}
