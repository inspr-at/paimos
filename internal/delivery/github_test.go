// SPDX-License-Identifier: AGPL-3.0-only
package delivery

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

	"github.com/inspr-at/paimos/internal/crossreview"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGitHubReaderUsesReadOnlyScopesAndFreshQueueHead(t *testing.T) {
	// Risk: observing checks must never inherit a status writer or claim a stale
	// synthetic merge group is still queued. All signing material is ephemeral.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "fixture.pem")
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	head, base, group := strings.Repeat("b", 40), strings.Repeat("a", 40), strings.Repeat("c", 40)
	ref := "refs/heads/gh-readonly-queue/main/pr-7-test"
	minted, revoked := 0, 0
	stale := false
	badChecks := false
	pr := map[string]any{"number": 7, "title": "AEON-848: delivery", "state": "open", "body": strings.Repeat("not retained", 10000), "head": map[string]string{"sha": head, "ref": "work/aeon-848-delivery"}, "base": map[string]any{"sha": base, "ref": "main", "repo": map[string]string{"full_name": "example/delivery"}}}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body any
		status := 200
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/456/access_tokens":
			var in struct {
				Permissions map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if len(in.Permissions) != 5 {
				t.Fatal("unexpected token permissions")
			}
			for _, p := range in.Permissions {
				if p != "read" {
					t.Fatal("observer minted a write token")
				}
			}
			body = map[string]any{"token": "delivery-fixture-token", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": "example/delivery"}}}
			minted++
		case r.Method == "DELETE" && r.URL.Path == "/installation/token":
			body = map[string]any{}
			revoked++
		case r.Method == "POST" && r.URL.Path == "/graphql":
			var in struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(in.Query, "query(") || strings.Contains(in.Query, "mutation") {
				t.Fatal("queue observer is not read-only")
			}
			body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"number": 7, "headRefOid": head, "mergeQueueEntry": map[string]any{"id": "queue-entry", "headCommit": map[string]string{"oid": group}}}}}}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/pulls/7":
			body = pr
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/pulls":
			body = []any{pr}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/check-runs"):
			runs := []any{}
			for _, n := range *defaults().RequiredChecks {
				runs = append(runs, map[string]any{"name": n, "status": "completed", "conclusion": "success"})
			}
			body = map[string]any{"total_count": 5, "check_runs": runs}
			if badChecks {
				status = 502
				body = map[string]string{"message": "provider unavailable"}
			}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/statuses"):
			body = []any{}
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/ref/"):
			sha := group
			if stale {
				sha = strings.Repeat("d", 40)
			}
			body = map[string]any{"ref": ref, "object": map[string]string{"type": "commit", "sha": sha}}
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/compare/"):
			body = map[string]any{"total_commits": 1, "status": "ahead", "commits": []any{map[string]any{"sha": group, "parents": []any{map[string]string{"sha": head}, map[string]string{"sha": base}}}}}
		default:
			t.Fatalf("unexpected GitHub operation: %s %s", r.Method, r.URL.Path)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	reader := AppReader{App: &crossreview.GitHubApp{Config: crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Repository: "example/delivery", KeyFile: keyFile}, Client: client}}
	p, err := reader.Pull(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Queued || p.QueueHead != group || len(p.Checks) != 5 {
		t.Fatal("queue/check observation incomplete")
	}
	if minted != 1 || revoked != 1 {
		t.Fatal("read token not revoked")
	}
	pulls, err := reader.Group(t.Context(), group, base, ref)
	if err != nil || len(pulls) != 1 {
		t.Fatal("current queue group not resolved", err)
	}
	stale = true
	if _, err = reader.Group(t.Context(), group, base, ref); err == nil {
		t.Fatal("replaced queue group accepted")
	}
	badChecks = true
	if _, err = reader.Pull(t.Context(), 7); err == nil {
		t.Fatal("partial check read reported success")
	}
	if minted != revoked {
		t.Fatal("failed observations leaked token lifetime")
	}
}
