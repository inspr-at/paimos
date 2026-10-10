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

func TestGitHubPublishPostsGateReason(t *testing.T) {
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
	model := "review-model"
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	pr := int64(12)
	binding := reviewgate.Binding{Repository: "example/review-fixture", BaseSHA: base, HeadSHA: head, PullRequest: &pr}
	longReason := "not cross-family: author and reviewer are both openai\n" + strings.Repeat("x", 180)
	for _, tc := range []struct {
		name, state, reason, github string
		open, denied                bool
		status                      string
	}{
		{name: "policy off", state: "success", reason: "cross-family not required (policy off)", open: true, status: "completed"},
		{name: "same family", state: "failure", reason: "not cross-family: author and reviewer are both openai", denied: true, status: "completed"},
		{name: "bounded reason", state: "failure", reason: longReason, denied: true, status: "completed"},
		{name: "pending keeps its sentence", state: "pending", reason: "not cross-family: author and reviewer are both openai", status: "running"},
		{name: "stale binding", state: "error", reason: "cross-family not required (policy off)", github: "stale", open: true, status: "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posted map[string]string
			config := AppConfig{ID: "1", InstallationID: "2", KeyFile: file, TenantID: testID(), Repository: binding.Repository}
			app := &GitHubApp{Config: config, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				body, code := "{}", 200
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/2/access_tokens":
					raw, _ := json.Marshal(map[string]any{"token": "fixture-only-token", "permissions": map[string]string{"statuses": "write", "pull_requests": "read", "metadata": "read"}, "repositories": []map[string]string{{"full_name": config.Repository}}})
					body = string(raw)
				case "GET /repos/example/review-fixture/pulls/12":
					body = `{"number":12,"head":{"sha":"` + head + `"},"base":{"sha":"` + base + `","repo":{"full_name":"example/review-fixture"}}}`
				case "POST /repos/example/review-fixture/statuses/" + head:
					posted = map[string]string{}
					if json.NewDecoder(r.Body).Decode(&posted) != nil {
						t.Fatal("status body")
					}
				case "DELETE /installation/token":
					code = 204
				default:
					t.Fatalf("unexpected operation %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
			})}}
			v := Review{
				Binding: binding, Status: tc.status, GateOpen: tc.open, GateReason: tc.reason,
				GitHubStatus: tc.github, policyDenied: tc.denied, modelEvidence: "vendor_reported",
				Model: &model, EffectiveModel: &model, Result: reviewgate.Result{Verdict: "ok"},
			}
			if statusState(v) != tc.state {
				t.Fatalf("status %s, want %s", statusState(v), tc.state)
			}
			got, err := app.Publish(context.Background(), config.TenantID, v, tc.state)
			wantReturn := tc.state
			if tc.github == "stale" {
				wantReturn = "stale"
			}
			if err != nil || got != wantReturn || posted == nil {
				t.Fatalf("publish %q %v posted=%v", got, err, posted != nil)
			}
			if posted["context"] != "aeon/review" || posted["description"] != statusDescription(v, tc.state) {
				t.Fatalf("description %q, want gate text %q", posted["description"], statusDescription(v, tc.state))
			}
			if tc.github == "stale" && posted["description"] != "Review binding differs from the pull request" {
				t.Fatal("stale binding lost its revocation sentence")
			}
			if tc.github != "stale" && (posted["description"] == "Pinned commit range passed cross-family review" || posted["description"] == "Independent review requires changes" || posted["description"] == "Independent review unavailable; gate closed") {
				t.Fatal("fixed status text replaced the gate reason")
			}
		})
	}
}
