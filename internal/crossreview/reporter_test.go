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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

type recordingPublisher struct {
	orders    []string
	fail      bool
	failOrder string
}

func (*recordingPublisher) Configured(string, string) bool { return true }
func (p *recordingPublisher) Publish(_ context.Context, _ string, v Review, state string) (string, error) {
	p.orders = append(p.orders, v.OrderID)
	if p.fail && (p.failOrder == "" || p.failOrder == v.OrderID) {
		return "error", errors.New("synthetic publication failure")
	}
	return state, nil
}

func (f *fixture) completedReview(t *testing.T, in CreateInput) Review {
	t.Helper()
	var v Review
	f.call(t, f.person, "POST", "/api/nodes/"+f.ticket+"/reviews", in, 201, &v)
	if v.RunID == nil {
		t.Fatal("fixture review was not queued")
	}
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed',effective_model='review-model',model_evidence='vendor_reported' WHERE id=$1`, *v.RunID); err != nil {
			return err
		}
		raw, _ := json.Marshal(reviewgate.Parse("VERDICT: ok"))
		if _, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET result=$2 WHERE work_order_id=$1`, v.OrderID, raw); err != nil {
			return err
		}
		var err error
		v, err = load(t.Context(), tx, v.OrderID)
		return err
	})
	if !v.GateOpen {
		t.Fatal("fixture did not open review gate")
	}
	return v
}

func TestReporterRechecksGreenBaseAndKeepsStaleClosed(t *testing.T) {
	f := newFixture(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("fixture signing key could not be generated")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "fixture-signing")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	in := f.input()
	pr := int64(12)
	in.PullRequest = &pr
	base := in.BaseSHA
	var posted []string
	checks := 0
	f.m.publisher = &GitHubApp{Config: AppConfig{ID: "1", InstallationID: "2", KeyFile: file, TenantID: f.person.TenantID, Repository: in.Repository}, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch r.Method + " " + r.URL.Path {
		case "POST /app/installations/2/access_tokens":
			body = `{"token":"fixture-only-token","permissions":{"statuses":"write","pull_requests":"read"},"repositories":[{"full_name":"example/review-fixture"}]}`
		case "GET /repos/example/review-fixture/pulls/12":
			checks++
			body = fmt.Sprintf(`{"number":12,"head":{"sha":%q},"base":{"sha":%q,"repo":{"full_name":"example/review-fixture"}}}`, in.HeadSHA, base)
		case "POST /repos/example/review-fixture/statuses/" + in.HeadSHA:
			var input map[string]string
			if json.NewDecoder(r.Body).Decode(&input) != nil || input["context"] != "aeon/review" {
				t.Fatal("invalid status")
			}
			posted = append(posted, input["state"])
		case "DELETE /installation/token":
		default:
			t.Fatal("unexpected GitHub operation")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	v := f.completedReview(t, in)
	f.m.reportStatuses(t.Context())
	if len(posted) != 1 || posted[0] != "success" {
		t.Fatalf("initial statuses: %v", posted)
	}
	// Each reporter return is the barrier; no timing assumptions or sleeps.
	f.m.reportStatuses(t.Context())
	if checks < 2 || len(posted) != 1 {
		t.Fatalf("unchanged success was not checked without reposting: checks=%d, posts=%v", checks, posted)
	}
	base = strings.Repeat("c", 40)
	f.m.reportStatuses(t.Context())
	if len(posted) != 2 || posted[1] != "error" {
		t.Fatalf("changed base kept a green status: %v", posted)
	}
	f.tx(t, func(tx pgx.Tx) error {
		v, err = load(t.Context(), tx, v.OrderID)
		return err
	})
	if v.GitHubStatus != "stale" || v.GateOpen {
		t.Fatal("stale review remained an approval")
	}
	base = in.BaseSHA
	f.m.reportStatuses(t.Context())
	if len(posted) != 2 {
		t.Fatal("stale review was revived without a new review")
	}
}

func TestReporterPagesPastReportedHeadsAndRetriesOlderFailure(t *testing.T) {
	f := newFixture(t)
	p := &recordingPublisher{}
	f.m.publisher = p
	var oldest Review
	for i := 0; i < 102; i++ {
		in := f.input()
		pr := int64(i + 1)
		in.PullRequest = &pr
		in.HeadSHA = fmt.Sprintf("%040x", i+1)
		v := f.completedReview(t, in)
		if i == 0 {
			oldest = v
		}
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET github_status='success',github_reported_state=CASE WHEN work_order_id=$1 THEN '' ELSE 'success' END`, oldest.OrderID)
		return err
	})
	p.fail = true
	p.failOrder = oldest.OrderID
	f.m.reportStatuses(t.Context())
	if len(p.orders) != 102 || p.orders[0] != oldest.OrderID {
		t.Fatalf("older dirty review was starved: %v", p.orders)
	}
	p.fail = false
	p.orders = nil
	f.m.reportStatuses(t.Context())
	if len(p.orders) != 103 || p.orders[0] != oldest.OrderID {
		t.Fatalf("older failure was not retried: %v", p.orders)
	}
	p.orders = nil
	f.m.reportStatuses(t.Context())
	if len(p.orders) != 102 || p.orders[101] != oldest.OrderID {
		t.Fatal("binding refresh did not traverse every page")
	}
}
