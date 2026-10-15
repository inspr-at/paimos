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

func TestAdmissionAppPinsHeadContextAndRepository(t *testing.T) {
	// Risk: the queue writer drops expectedHeadOid, uses auto-merge, accepts an
	// overprivileged/wrong-repository token, or publishes another context.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fixture-signing")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("b", 40)
	mutations, posts, revokes := 0, 0, 0
	excess, wrongRepo, wrongHead, graphqlError := false, false, false, false
	app := &GitHubApp{Config: AppConfig{ID: "1", InstallationID: "2", KeyFile: path, TenantID: testID(), Repository: "example/admission"}}
	app.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body any = map[string]any{}
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/2/access_tokens":
			var in struct {
				Repositories []string
				Permissions  map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if len(in.Repositories) != 1 || in.Repositories[0] != "admission" {
				t.Fatal("unbounded repository token")
			}
			if excess {
				in.Permissions["actions"] = "write"
			}
			repo := "example/admission"
			if wrongRepo {
				repo = "other/admission"
			}
			body = map[string]any{"token": "fixture-only-token", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": repo}}}
		case r.Method == "DELETE" && r.URL.Path == "/installation/token":
			revokes++
		case r.Method == "POST" && r.URL.Path == "/graphql":
			var in struct {
				Query     string
				Variables map[string]json.RawMessage
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(in.Query, "query") {
				actual := head
				if wrongHead {
					actual = strings.Repeat("c", 40)
				}
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"id": "PR_fixture", "number": 7, "headRefOid": actual}}}}
			} else {
				var input map[string]string
				if err := json.Unmarshal(in.Variables["input"], &input); err != nil {
					t.Fatal(err)
				}
				if input["pullRequestId"] != "PR_fixture" {
					t.Fatal("wrong pull ID")
				}
				name := "dequeuePullRequest"
				if strings.Contains(in.Query, "EnqueuePullRequestInput") {
					name = "enqueuePullRequest"
					if input["expectedHeadOid"] != head {
						t.Fatal("missing atomic head pin")
					}
				} else if !strings.Contains(in.Query, "DequeuePullRequestInput") {
					t.Fatal("unexpected queue mutation")
				}
				mutations++
				body = map[string]any{"data": map[string]any{name: map[string]string{"clientMutationId": "aeon-admission"}}}
				if graphqlError {
					body = map[string]any{"errors": []any{map[string]string{"message": "fixture refusal"}}}
				}
			}
		case r.Method == "POST" && r.URL.Path == "/repos/example/admission/statuses/"+head:
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if in["context"] != "aeon/admission" || in["state"] != "failure" {
				t.Fatal("wrong status context/state")
			}
			posts++
		case r.Method == "GET" && r.URL.Path == "/repos/example/admission/commits/"+head+"/statuses":
			body = []any{map[string]string{"context": "unrelated", "state": "success"}, map[string]string{"context": "aeon/admission", "state": "failure"}}
		default:
			t.Fatalf("unexpected operation %s %s", r.Method, r.URL.Path)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	ctx := t.Context()
	if err = app.AdmissionQueue(ctx, 7, head, true); err != nil {
		t.Fatal(err)
	}
	if err = app.AdmissionQueue(ctx, 7, "", false); err != nil {
		t.Fatal(err)
	}
	state := "failure"
	if _, err = app.AdmissionStatus(ctx, head, &state); err != nil {
		t.Fatal(err)
	}
	if state, err = app.AdmissionStatus(ctx, head, nil); err != nil || state != "failure" {
		t.Fatalf("status read %q %v", state, err)
	}
	if mutations != 2 || posts != 1 || revokes != 4 {
		t.Fatalf("operations: %d %d %d", mutations, posts, revokes)
	}
	for _, kind := range []string{"permissions", "repository", "head", "graphql"} {
		excess = kind == "permissions"
		wrongRepo = kind == "repository"
		wrongHead = kind == "head"
		graphqlError = kind == "graphql"
		if err = app.AdmissionQueue(ctx, 7, head, true); err == nil {
			t.Fatalf("accepted invalid %s", kind)
		}
	}
	if mutations != 3 || posts != 1 {
		t.Fatal("refusal performed unauthorized writes")
	}
}
