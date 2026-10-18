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

func TestDeliveryShippingAppReaderScopesBindingsAndPartialReads(t *testing.T) {
	// Risk: a shadow read inherits PR/queue/actions write rights, reads an
	// unrelated branch/run attempt, or a partial read reports ready to enqueue.
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
	head, base := strings.Repeat("b", 40), strings.Repeat("a", 40)
	branch := "work/aeon-891-shipapp"
	hasPR, badJobs, moved := false, false, false
	minted, revoked, headReads := 0, 0, 0
	pr := map[string]any{"number": 7, "title": "AEON-891: ship", "state": "open", "draft": false, "mergeable": true, "mergeable_state": "clean", "head": map[string]string{"sha": head, "ref": branch}, "base": map[string]any{"sha": base, "ref": "main", "repo": map[string]string{"full_name": "example/delivery"}}}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body any
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/456/access_tokens":
			var in struct {
				Permissions  map[string]string `json:"permissions"`
				Repositories []string          `json:"repositories"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if len(in.Permissions) != 6 || in.Permissions["actions"] != "read" || len(in.Repositories) != 1 || in.Repositories[0] != "delivery" {
				t.Fatal("shipping token scopes or repository drift")
			}
			for _, level := range in.Permissions {
				if level != "read" {
					t.Fatal("shadow requested write authority")
				}
			}
			body = map[string]any{"token": "delivery-test-fixture", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": "example/delivery"}}}
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
				t.Fatal("shadow queue mutation")
			}
			body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"number": 7, "headRefOid": head, "mergeQueueEntry": nil}}}}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery":
			body = map[string]string{"full_name": "example/delivery", "default_branch": "main"}
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/ref/heads/"):
			ref := "main"
			sha := base
			if strings.HasSuffix(r.URL.Path, branch) {
				ref = branch
				sha = head
				headReads++
				if moved && headReads%2 == 0 {
					sha = strings.Repeat("c", 40)
				}
			}
			body = map[string]any{"ref": "refs/heads/" + ref, "object": map[string]string{"type": "commit", "sha": sha}}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/pulls":
			if r.URL.Query().Get("head") != "example:"+branch {
				t.Fatal("unbound PR inventory")
			}
			body = []any{}
			if hasPR {
				body = []any{pr}
			}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/pulls/7":
			body = pr
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/compare/"):
			body = map[string]any{"status": "ahead", "ahead_by": 1, "behind_by": 0}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/check-runs"):
			runs := []any{}
			for _, name := range *defaults().RequiredChecks {
				runs = append(runs, map[string]string{"name": name, "status": "completed", "conclusion": "success"})
			}
			body = map[string]any{"total_count": len(runs), "check_runs": runs}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/statuses"):
			body = []any{}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/actions/runs":
			if r.URL.Query().Get("head_sha") != head {
				t.Fatal("workflow lookup lacked exact head")
			}
			body = map[string]any{"total_count": 1, "workflow_runs": []any{map[string]any{"id": 42, "workflow_id": 10, "run_attempt": 2, "head_sha": head, "name": "CI", "event": "pull_request", "status": "completed", "conclusion": "failure"}}}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/actions/runs/42/attempts/2/jobs":
			jobHead := head
			if badJobs {
				jobHead = strings.Repeat("c", 40)
			}
			jobs := []any{}
			for _, job := range []Check{{Name: "go", Status: "completed", Conclusion: "failure"}, {Name: "go-test (2)", Status: "completed", Conclusion: "timed_out"}} {
				jobs = append(jobs, map[string]any{"id": len(jobs) + 1, "run_id": 42, "run_attempt": 2, "head_sha": jobHead, "name": job.Name, "status": job.Status, "conclusion": job.Conclusion})
			}
			body = map[string]any{"total_count": len(jobs), "jobs": jobs}
		default:
			t.Fatalf("unexpected shipping operation: %s %s", r.Method, r.URL.Path)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	reader := AppReader{App: &crossreview.GitHubApp{Config: crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Repository: "example/delivery", KeyFile: keyFile}, Client: client}}
	facts, err := reader.Shipping(t.Context(), branch, head)
	if err != nil || facts.PR != nil || facts.PushedHead != head || facts.Base != base {
		t.Fatalf("no-PR observation: %+v %v", facts, err)
	}
	hasPR = true
	facts, err = reader.Shipping(t.Context(), branch, head)
	if err != nil {
		t.Fatal(err)
	}
	out := ShipDecision{ShipInput: ShipInput{Head: head}, Facts: facts}
	proposeShip(&out, Round{}, defaults(), false)
	if out.Action != "rerun_failed" || out.Run == nil || *out.Run != 42 || out.RunAttempt != 2 {
		t.Fatalf("reader dropped shard evidence: %+v", out)
	}
	badJobs = true
	if _, err = reader.Shipping(t.Context(), branch, head); err == nil {
		t.Fatal("stale shard head accepted")
	}
	badJobs = false
	moved = true
	if _, err = reader.Shipping(t.Context(), branch, head); err == nil {
		t.Fatal("mixed branch heads accepted")
	}
	if minted != 4 || revoked != minted {
		t.Fatal("read tokens not revoked on success and failure")
	}
}
