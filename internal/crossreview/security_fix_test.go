// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func TestAuthenticatedBaseChangeBypassesDirtyReporter(t *testing.T) {
	s := newStatusFixture(t)
	v := s.f.completedReview(t, s.in)
	s.f.m.reportStatuses(t.Context())
	configure, ok := any(s.f.m).(interface{ ConfigureWebhook([]byte) })
	if !ok {
		t.Fatal("no authenticated PR change path; revocation waits for dirty reporter")
	}
	secret := []byte("synthetic-webhook-fixture-32-bytes")
	configure.ConfigureWebhook(secret)
	dirty := s.in
	dirty.RequestID, dirty.HeadSHA = testID(), strings.Repeat("d", 40)
	s.f.completedReview(t, dirty)
	app := s.f.m.publisher.(*GitHubApp)
	baseTransport := app.Client.Transport
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/statuses/"+dirty.HeadSHA) {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return nil, fmt.Errorf("synthetic dirty publication failure")
		}
		return baseTransport.RoundTrip(r)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); close(release); <-done }()
	go func() { defer close(done); s.f.m.reportStatuses(ctx) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("dirty publisher did not reach barrier")
	}
	s.base = strings.Repeat("c", 40)
	raw := fmt.Sprintf(`{"action":"edited","installation":{"id":2},"repository":{"full_name":"example/review-fixture"},"pull_request":{"number":12,"head":{"sha":%q},"base":{"sha":%q,"repo":{"full_name":"example/review-fixture"}}}}`, s.in.HeadSHA, s.base)
	sign := hmac.New(sha256.New, secret)
	sign.Write([]byte(raw))
	r := httptest.NewRequest("POST", "/api/reviews/github", strings.NewReader(raw))
	r.Header.Set("X-GitHub-Event", "pull_request")
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(sign.Sum(nil)))
	w := httptest.NewRecorder()
	s.f.mux.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("authenticated signal returned %d", w.Code)
	}
	if len(s.posts) != 2 || s.posts[1] != "error" {
		t.Fatalf("dirty reporter delayed revocation: %v", s.posts)
	}
	s.f.tx(t, func(tx pgx.Tx) error {
		got, err := load(t.Context(), tx, v.OrderID)
		if err == nil && got.GateOpen {
			t.Error("revoked gate remains open")
		}
		return err
	})
}

type statusFixture struct {
	f                *fixture
	in               CreateInput
	base, repository string
	number           int64
	fail             bool
	posts            []string
}

func newStatusFixture(t *testing.T) *statusFixture {
	t.Helper()
	s := &statusFixture{f: newFixture(t), number: 12}
	s.in = s.f.input()
	pr := int64(12)
	s.in.PullRequest = &pr
	s.base, s.repository = s.in.BaseSHA, s.in.Repository
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("fixture signing key generation failed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "fixture-signing")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	s.f.m.publisher = &GitHubApp{Config: AppConfig{ID: "1", InstallationID: "2", KeyFile: file, TenantID: s.f.person.TenantID, Repository: s.in.Repository}, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body, code := "{}", 200
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/2/access_tokens":
			body = `{"token":"fixture-only-token","permissions":{"statuses":"write","pull_requests":"read"},"repositories":[{"full_name":"example/review-fixture"}]}`
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/example/review-fixture/pulls/"):
			body = fmt.Sprintf(`{"number":%d,"head":{"sha":%q},"base":{"sha":%q,"repo":{"full_name":%q}}}`, s.number, s.in.HeadSHA, s.base, s.repository)
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/repos/example/review-fixture/statuses/"):
			var input map[string]string
			if json.NewDecoder(r.Body).Decode(&input) != nil || input["context"] != "aeon/review" {
				t.Error("invalid status")
			}
			if s.fail {
				code = 503
			} else {
				s.posts = append(s.posts, input["state"])
			}
		case r.Method == "DELETE" && r.URL.Path == "/installation/token":
		default:
			t.Errorf("unexpected GitHub operation: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	return s
}

func TestUnpublishableReviewCannotHideGreenStatusOwner(t *testing.T) {
	s := newStatusFixture(t)
	s.f.completedReview(t, s.in)
	s.f.m.reportStatuses(t.Context())
	if len(s.posts) != 1 || s.posts[0] != "success" {
		t.Fatal("initial success missing")
	}
	in := s.in
	in.RequestID, in.PullRequest = testID(), nil
	s.f.completedReview(t, in)
	s.base = strings.Repeat("c", 40)
	s.f.m.reportStatuses(t.Context())
	if len(s.posts) != 2 || s.posts[1] != "error" {
		t.Fatalf("null-PR review hid status owner: %v", s.posts)
	}
}

func TestStaleBindingPostsErrorAndRetriesUntilRecorded(t *testing.T) {
	for _, kind := range []string{"number", "repository", "base"} {
		t.Run(kind, func(t *testing.T) {
			s := newStatusFixture(t)
			v := s.f.completedReview(t, s.in)
			s.f.m.reportStatuses(t.Context())
			switch kind {
			case "number":
				s.number = 13
			case "repository":
				s.repository = "example/another"
			case "base":
				s.base = strings.Repeat("c", 40)
			}
			s.fail = true
			s.f.m.reportStatuses(t.Context())
			if len(s.posts) != 1 {
				t.Fatal("failed publication recorded as posted")
			}
			s.fail = false
			s.f.m.reportStatuses(t.Context())
			if len(s.posts) != 2 || s.posts[1] != "error" {
				t.Fatalf("stale green was not revoked on retry: %v", s.posts)
			}
			s.f.tx(t, func(tx pgx.Tx) error {
				var status, state string
				if err := tx.QueryRow(t.Context(), `SELECT github_status,github_reported_state FROM work_order_reviews WHERE work_order_id=$1`, v.OrderID).Scan(&status, &state); err != nil {
					return err
				}
				if status != "stale" || state != "error" {
					t.Errorf("recorded %s/%s, want stale/error", status, state)
				}
				return nil
			})
		})
	}
}

func TestQueueReviewRejectsLegacyProfileFamily(t *testing.T) {
	f := newFixture(t)
	var o workorders.Order
	f.call(t, f.person, "POST", "/api/work-orders", workorders.CreateInput{Title: "Legacy review", Parent: &f.ticket, Assignee: &f.agent.ID, Criteria: []string{"Review independently"}}, 201, &o)
	f.tx(t, func(tx pgx.Tx) error {
		profile := testID()
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'legacy-queue','1','claude','xai','review-model','xhigh','frontier')`, f.person.TenantID, profile); err != nil {
			return err
		}
		family := "xai"
		o.Kind, o.Status = "review", "ready"
		o.Review = &reviewgate.Binding{ProfileID: &profile, ReviewerFamily: &family, AuthorFamily: "anthropic"}
		if _, err := agentruns.QueueReview(t.Context(), tx, f.person, o, f.agent.ID, profile, f.account); err == nil {
			t.Error("QueueReview trusted a legacy family label")
		}
		return nil
	})
}
