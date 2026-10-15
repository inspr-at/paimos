// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
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
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/reviewgate"
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
			body = map[string]any{"merge_base_commit": map[string]string{"sha": base}, "files": []any{map[string]string{"filename": "docs/fixture.md", "status": "modified"}}, "total_commits": 1, "status": "ahead", "commits": []any{map[string]any{"sha": group, "parents": []any{map[string]string{"sha": head}, map[string]string{"sha": base}}}}}
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
	if err != nil || len(pulls) != 1 || len(pulls[0].Checks) != 5 {
		t.Fatal("queue group dropped required-check success", err)
	}
	for _, c := range pulls[0].Checks {
		if c.Status != "completed" || c.Conclusion != "success" {
			t.Fatal("grouped pull stored a required check that was not success")
		}
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

func TestGitHubReaderConfirmsMissingPull(t *testing.T) {
	// Risk: a deleted pull is a confirmed absence. A transport failure on the
	// pull document must stay a failed read, not look like the pull is gone.
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
	pr := map[string]any{"number": 7, "title": "AEON-848: delivery", "state": "open", "head": map[string]string{"sha": head, "ref": "work/aeon-848-delivery"}, "base": map[string]any{"sha": base, "ref": "main", "repo": map[string]string{"full_name": "example/delivery"}}}
	mode := "missing"
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
			body = map[string]any{"token": "delivery-fixture-token", "permissions": in.Permissions, "repositories": []any{map[string]string{"full_name": "example/delivery"}}}
		case r.Method == "DELETE" && r.URL.Path == "/installation/token":
			body = map[string]any{}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/pulls/7":
			body = pr
			if mode == "missing" {
				status = 404
				body = map[string]string{"message": "Not Found"}
			}
			if mode == "down" {
				status = 502
				body = map[string]string{"message": "unavailable"}
			}
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/compare/"):
			body = map[string]any{"merge_base_commit": map[string]string{"sha": base}, "files": []any{}}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/check-runs"):
			body = map[string]any{"total_count": 0, "check_runs": []any{}}
			if mode == "partial" {
				status = 404
				body = map[string]string{"message": "Not Found"}
			}
		default:
			t.Fatalf("unexpected GitHub operation: %s %s", r.Method, r.URL.Path)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	reader := AppReader{App: &crossreview.GitHubApp{Config: crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Repository: "example/delivery", KeyFile: keyFile}, Client: client}}
	if _, err = reader.Pull(t.Context(), 7); !errors.Is(err, errMissing) || webhookErrorClass(err) != "http_404" {
		t.Fatal("deleted pull lost its confirmed absence or HTTP diagnostic", err)
	}
	mode = "down"
	if _, err = reader.Pull(t.Context(), 7); err == nil || errors.Is(err, errMissing) {
		t.Fatal("pull transport failure looked like absence", err)
	}
	mode = "partial"
	if _, err = reader.Pull(t.Context(), 7); err == nil || errors.Is(err, errMissing) {
		t.Fatal("partial check read looked like a missing pull", err)
	}
}

// observedAdmission applies the same scope fact AdmissionTx stores: a missing
// diff cannot match the binding, and an incomplete diff cannot skip visual review.
func observedAdmission(p Pull) AdmissionDecision {
	base := p.Base
	diff := AdmissionDiff{}
	if p.AdmissionDiff != nil {
		diff = *p.AdmissionDiff
		if reviewgate.ValidSHA(diff.MergeBase) {
			base = diff.MergeBase
		}
	}
	in := approvedAdmission()
	in.Binding.Repository = "example/delivery"
	in.Binding.PR = p.Number
	in.Binding.Target = p.Target
	in.Binding.Base = base
	in.Binding.Head = p.Head
	in.Binding.Scope = base + ".." + p.Head
	in.Review.Binding = in.Binding
	in.Diff = diff
	in.Open = p.Open
	in.ScopeVerified = diff.MergeBase == in.Binding.Base && reviewgate.ValidSHA(diff.MergeBase)
	return EvaluateAdmission(in)
}

func TestOpenPullCompareFailureStillObservesRepository(t *testing.T) {
	// Risk: one open pull whose compare fails, lacks a merge-base, or names a
	// path git allows and admission rejects stops observation of every pull.
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
	base, goodHead, badHead, group := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("d", 40), strings.Repeat("c", 40)
	ref := "refs/heads/gh-readonly-queue/main/pr-8-test"
	pullDoc := func(number int64, head, branch, title string) map[string]any {
		return map[string]any{"number": number, "title": title, "state": "open", "head": map[string]string{"sha": head, "ref": branch}, "base": map[string]any{"sha": base, "ref": "main", "repo": map[string]string{"full_name": "example/delivery"}}}
	}
	good := pullDoc(7, goodHead, "work/aeon-1392-sibling", "AEON-1392: sibling")
	bad := pullDoc(8, badHead, "work/aeon-1392-damaged", "AEON-1392: damaged")
	mode := "down"
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
		case r.Method == "DELETE" && r.URL.Path == "/installation/token":
			body = map[string]any{}
		case r.Method == "POST" && r.URL.Path == "/graphql":
			var in struct {
				Query     string `json:"query"`
				Variables struct {
					Number int64 `json:"number"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(in.Query, "mutation") {
				t.Fatal("queue observer is not read-only")
			}
			head := goodHead
			if in.Variables.Number == 8 {
				head = badHead
			} else if in.Variables.Number != 7 {
				t.Fatalf("queue read for unexpected pull %d", in.Variables.Number)
			}
			body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"number": in.Variables.Number, "headRefOid": head}}}}
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/pulls/8":
			body = bad
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/pulls/7":
			body = good
		case r.Method == "GET" && r.URL.Path == "/repos/example/delivery/pulls":
			body = []any{bad, good}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/check-runs"):
			runs := []any{}
			for _, n := range *defaults().RequiredChecks {
				runs = append(runs, map[string]any{"name": n, "status": "completed", "conclusion": "success"})
			}
			body = map[string]any{"total_count": len(runs), "check_runs": runs}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/statuses"):
			body = []any{}
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/ref/"):
			body = map[string]any{"ref": ref, "object": map[string]string{"type": "commit", "sha": group}}
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/compare/") && strings.Contains(r.URL.Path, badHead):
			switch mode {
			case "down":
				status = 502
				body = map[string]string{"message": "provider unavailable"}
			case "merge-base":
				body = map[string]any{"merge_base_commit": map[string]string{"sha": "not-a-commit"}, "files": []any{map[string]string{"filename": "docs/a.md", "status": "modified"}}}
			case "odd-name":
				body = map[string]any{"merge_base_commit": map[string]string{"sha": base}, "files": []any{map[string]string{"filename": "docs/odd\nname.md", "status": "modified"}}}
			default:
				t.Fatal(mode)
			}
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/compare/"):
			body = map[string]any{"merge_base_commit": map[string]string{"sha": base}, "files": []any{map[string]string{"filename": "docs/fixture.md", "status": "modified"}}, "total_commits": 2, "status": "ahead", "commits": []any{map[string]any{"sha": goodHead, "parents": []any{map[string]string{"sha": base}}}, map[string]any{"sha": badHead, "parents": []any{map[string]string{"sha": base}}}}}
		default:
			t.Fatalf("unexpected GitHub operation: %s %s", r.Method, r.URL.Path)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	reader := AppReader{App: &crossreview.GitHubApp{Config: crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Repository: "example/delivery", KeyFile: keyFile}, Client: client}}
	checks := len(*defaults().RequiredChecks)
	for _, mode = range []string{"down", "merge-base", "odd-name"} {
		t.Run(mode, func(t *testing.T) {
			pulls, err := reader.OpenPulls(t.Context())
			if err != nil || len(pulls) != 2 {
				t.Fatal("one compare stopped repository observation", err, len(pulls))
			}
			var sibling, damaged Pull
			for _, p := range pulls {
				switch p.Number {
				case 7:
					sibling = p
				case 8:
					damaged = p
				default:
					t.Fatalf("unexpected pull %d", p.Number)
				}
			}
			if sibling.Number != 7 || damaged.Number != 8 || len(sibling.Checks) != checks || len(damaged.Checks) != checks {
				t.Fatal("sibling or damaged observation incomplete", sibling.Number, damaged.Number, len(sibling.Checks), len(damaged.Checks))
			}
			siblingDecision := observedAdmission(sibling)
			if sibling.AdmissionDiff == nil || !sibling.AdmissionDiff.Complete || sibling.AdmissionDiff.MergeBase != base || !siblingDecision.Admissible || siblingDecision.Reason != "admissible" {
				t.Fatalf("sibling observation lost: %+v %+v", sibling.AdmissionDiff, siblingDecision)
			}
			damagedDecision := observedAdmission(damaged)
			if damagedDecision.Admissible {
				t.Fatalf("damaged pull admitted: %+v", damagedDecision)
			}
			switch mode {
			case "down", "merge-base":
				if damaged.AdmissionDiff != nil || damagedDecision.Reason != "scope_unverified" {
					t.Fatalf("missing compare kept evidence: %+v %+v", damaged.AdmissionDiff, damagedDecision)
				}
			case "odd-name":
				if damaged.AdmissionDiff == nil || damaged.AdmissionDiff.Complete || damaged.AdmissionDiff.MergeBase != base || damagedDecision.Reason != "visual_missing_or_stale" || !damagedDecision.VisualRequired || damagedDecision.VisualReason != "diff_unproven" {
					t.Fatalf("odd filename aborted or looked proven: %+v %+v", damaged.AdmissionDiff, damagedDecision)
				}
				for _, path := range damaged.AdmissionDiff.Paths {
					if !validAdmissionPath(path) {
						t.Fatalf("stored unusable path %q", path)
					}
				}
			}
			one, err := reader.Pull(t.Context(), 8)
			if err != nil || one.Number != 8 || len(one.Checks) != checks {
				t.Fatal("single damaged pull stopped its own observation", err)
			}
			if (one.AdmissionDiff == nil) != (damaged.AdmissionDiff == nil) {
				t.Fatal("pull and inventory disagreed about missing evidence")
			}
			grouped, err := reader.Group(t.Context(), group, base, ref)
			if err != nil || len(grouped) != 2 {
				t.Fatal("one compare stopped the merge group", err, len(grouped))
			}
		})
	}
}
