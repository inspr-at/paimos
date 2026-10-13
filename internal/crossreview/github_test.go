// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGitHubStatusExactHeadAndNarrowAuthority(t *testing.T) {
	// Risk: opaque provider tokens prevent status publication, or relaxing their
	// length check also accepts forbidden characters or widens authority.
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
		token           string
		want            string
		posts           int
	}{
		{name: "exact", head: strings.Repeat("b", 40), base: strings.Repeat("a", 40), want: "success", posts: 1},
		{name: "stale", head: strings.Repeat("c", 40), base: strings.Repeat("a", 40), want: "stale", posts: 1},
		{name: "excess authority", head: strings.Repeat("b", 40), base: strings.Repeat("a", 40), extraPermission: true, want: "error"},
		{name: "wrong base", head: strings.Repeat("b", 40), base: strings.Repeat("c", 40), want: "stale", posts: 1},
		{name: "opaque token", head: strings.Repeat("b", 40), base: strings.Repeat("a", 40), token: "opaque_" + strings.Repeat("A1b_.-", 85) + "xyz", want: "success", posts: 1},
		{name: "forbidden character", head: strings.Repeat("b", 40), base: strings.Repeat("a", 40), token: strings.Repeat("a", 519) + "/", want: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := AppConfig{ID: "1", InstallationID: "2", KeyFile: file, TenantID: testID(), Repository: "example/review-fixture"}
			token := tc.token
			if token == "" {
				token = "fixture-only-token"
			}
			minted, reads, posts, revokes := 0, 0, 0, 0
			app := &GitHubApp{Config: config, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.github.com" || r.URL.Scheme != "https" {
					t.Fatal("unexpected host")
				}
				body := "{}"
				code := 200
				if r.URL.Path != "/app/installations/2/access_tokens" && r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatal("publisher did not forward the opaque minted token")
				}
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/2/access_tokens":
					minted++
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
					raw, _ := json.Marshal(map[string]any{"token": token, "permissions": permissions, "repositories": []map[string]string{{"full_name": config.Repository}}})
					body = string(raw)
					code = http.StatusCreated
				case "GET /repos/example/review-fixture/pulls/12":
					reads++
					body = `{"number":12,"head":{"sha":"` + tc.head + `"},"base":{"sha":"` + tc.base + `","repo":{"full_name":"example/review-fixture"}}}`
				case "POST /repos/example/review-fixture/statuses/" + strings.Repeat("b", 40):
					posts++
					var input map[string]string
					if json.NewDecoder(r.Body).Decode(&input) != nil || input["state"] != map[bool]string{true: "error", false: "success"}[tc.want == "stale"] || input["context"] != "aeon/review" {
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
			wantReads, wantRevokes := 1, 1
			if tc.extraPermission {
				wantReads = 0
			}
			if tc.name == "forbidden character" {
				wantReads, wantRevokes = 0, 0
			}
			if got != tc.want || (err != nil) != (tc.want == "error") || minted != 1 || reads != wantReads || posts != tc.posts || revokes != wantRevokes {
				t.Fatal("exact-head or token-lifetime protection failed")
			}
			if tc.want == "error" && !errors.Is(err, ErrUnavailable) {
				t.Fatal("publisher refused for a different reason")
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
func TestGitHubInstallationAcceptsOpaqueTokens(t *testing.T) {
	// Risk: provider token formats break delivery reads or routine PRs, while
	// accepting forbidden characters or changing mint authority would be unsafe.
	// Signing material and tokens are synthetic; all HTTP operations stay fake.
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
		t.Fatal("fixture signing material could not be written")
	}
	for _, path := range []string{"reader", "routine read", "routine write"} {
		t.Run(path, func(t *testing.T) {
			for _, tc := range []struct {
				name, token string
				valid       bool
			}{
				{"opaque", "opaque_" + strings.Repeat("A1b_.-", 85) + "xyz", true},
				{"longer opaque", "opaque_" + strings.Repeat("a", 4089), true},
				{"forbidden character", strings.Repeat("a", 519) + "/", false},
				{"too short", "short", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					config := AppConfig{ID: "1", InstallationID: "2", KeyFile: file, TenantID: testID(), Repository: "example/review-fixture"}
					scopes := map[string]string{"pull_requests": "read", "contents": "read", "metadata": "read"}
					if path == "reader" {
						scopes["checks"], scopes["statuses"] = "read", "read"
					} else if path == "routine write" {
						scopes["pull_requests"] = "write"
					}
					minted, callbacks, reads, posts, revoked := 0, 0, 0, 0, 0
					app := &GitHubApp{Config: config, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
						if r.URL.Host != "api.github.com" || r.URL.Scheme != "https" {
							t.Fatal("unexpected host")
						}
						if r.URL.Path != "/app/installations/2/access_tokens" && r.Header.Get("Authorization") != "Bearer "+tc.token {
							t.Fatal("operation did not forward the opaque minted token")
						}
						var body any
						code := http.StatusOK
						switch r.Method + " " + r.URL.Path {
						case "POST /app/installations/2/access_tokens":
							var in struct {
								Repositories []string          `json:"repositories"`
								Permissions  map[string]string `json:"permissions"`
							}
							if json.NewDecoder(r.Body).Decode(&in) != nil || !reflect.DeepEqual(in.Repositories, []string{"review-fixture"}) || !reflect.DeepEqual(in.Permissions, scopes) {
								t.Fatal("installation authority changed")
							}
							minted++
							code = http.StatusCreated
							body = map[string]any{"token": tc.token, "permissions": in.Permissions, "repositories": []map[string]string{{"full_name": config.Repository}}}
						case "GET /repos/example/review-fixture/pulls/12":
							reads++
							body = map[string]int{"number": 12}
						case "POST /repos/example/review-fixture/pulls":
							if path != "routine write" {
								t.Fatal("read-only installation wrote a PR")
							}
							posts++
							code = http.StatusCreated
							body = map[string]int{"number": 12}
						case "DELETE /installation/token":
							revoked++
							code = http.StatusNoContent
						default:
							t.Fatal("unexpected operation")
						}
						raw, err := json.Marshal(body)
						if err != nil {
							t.Fatal("invalid fake response")
						}
						return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header), Request: r}, nil
					})}}
					read := func(get func(string, any) error) error {
						var out struct {
							Number int `json:"number"`
						}
						if err := get("/pulls/12", &out); err != nil {
							return err
						}
						if out.Number != 12 {
							t.Fatal("reader did not return the bound PR")
						}
						return nil
					}
					var err error
					if path == "reader" {
						err = app.ReadInstallation(t.Context(), func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
							callbacks++
							return read(get)
						})
					} else {
						err = app.RoutinePullInstallation(t.Context(), config.Repository, path == "routine write", func(get func(string, any) error, create func(any, any) error) error {
							callbacks++
							if path == "routine write" {
								if err := create(map[string]string{"title": "fixture", "head": "fixture", "base": "main"}, nil); err != nil {
									return err
								}
							} else if !errors.Is(create(nil, nil), ErrUnavailable) {
								t.Fatal("read-only installation did not refuse a PR write")
							}
							return read(get)
						})
					}
					if !tc.valid {
						if !errors.Is(err, ErrUnavailable) || minted != 1 || callbacks != 0 || reads != 0 || posts != 0 || revoked != 0 {
							t.Fatal("invalid token was not refused immediately after minting")
						}
						return
					}
					wantPosts := 0
					if path == "routine write" {
						wantPosts = 1
					}
					if err != nil || minted != 1 || callbacks != 1 || reads != 1 || posts != wantPosts || revoked != 1 {
						t.Fatal("opaque token did not complete its scoped operation and revocation")
					}
				})
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
