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

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/workorders"
)

type blockingPublisher struct {
	dirty              string
	entered, refreshed chan struct{}
}

func (*blockingPublisher) Configured(string, string) bool { return true }
func (p *blockingPublisher) Publish(ctx context.Context, _ string, v Review, state string) (string, error) {
	if v.OrderID == p.dirty {
		close(p.entered)
		<-ctx.Done()
		return "error", ctx.Err()
	}
	select {
	case p.refreshed <- struct{}{}:
	default:
	}
	return state, nil
}
func TestBindingRefreshRunsWhileDirtyPublicationIsBlocked(t *testing.T) {
	f := newFixture(t)
	p := &blockingPublisher{entered: make(chan struct{}), refreshed: make(chan struct{}, 1)}
	f.m.publisher = p
	in := f.input()
	pr := int64(12)
	in.PullRequest = &pr
	green := f.completedReview(t, in)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET github_status='success',github_reported_state='success' WHERE work_order_id=$1`, green.OrderID)
		return err
	})
	in.RequestID, in.HeadSHA = testID(), strings.Repeat("d", 40)
	p.dirty = f.completedReview(t, in).OrderID
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() { defer close(done); f.m.RunStatusReporter(ctx) }()
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("dirty lane did not start")
	}
	select {
	case <-p.refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("dirty lane delayed green refresh")
	}
}

func signedPullRequest(raw string, secret []byte) *http.Request {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(raw))
	r := httptest.NewRequest("POST", "/api/reviews/github", strings.NewReader(raw))
	r.Header.Set("X-GitHub-Event", "pull_request")
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return r
}

func TestWebhookAuthenticationReplayAndFailedRevocation(t *testing.T) {
	s := newStatusFixture(t)
	v := s.f.completedReview(t, s.in)
	s.f.m.reportStatuses(t.Context())
	secret := []byte("synthetic-webhook-fixture-32-bytes")
	s.f.m.ConfigureWebhook(secret)
	authMod, err := auth.New(auth.Config{Env: "dev", SessionKey: []byte(strings.Repeat("fixture", 8))}, s.f.d.App)
	if err != nil {
		t.Fatal(err)
	}
	server := (&httpapi.Server{Modules: []httpapi.Module{s.f.m}, Middleware: []func(http.Handler) http.Handler{authMod.Middleware}}).Handler()
	raw := fmt.Sprintf(`{"action":"edited","installation":{"id":2},"repository":{"full_name":"example/review-fixture"},"pull_request":{"number":12,"head":{"sha":%q},"base":{"sha":%q,"repo":{"full_name":"example/review-fixture"}}}}`, s.in.HeadSHA, strings.Repeat("c", 40))
	for _, tc := range []struct {
		name, body string
		key        []byte
		code       int
	}{
		{"unsigned", raw, nil, 401},
		{"wrong signature", raw, []byte("other-fixture"), 401},
		{"wrong installation", strings.Replace(raw, `"id":2`, `"id":3`, 1), secret, 401},
		{"foreign repository", strings.Replace(raw, "example/review-fixture", "example/foreign", 1), secret, 401},
		{"foreign base", strings.ReplaceAll(raw, `"repo":{"full_name":"example/review-fixture"}`, `"repo":{"full_name":"example/foreign"}`), secret, 401},
		{"unsupported action", strings.Replace(raw, "edited", "labeled", 1), secret, 400},
		{"bad JSON", raw + "{}", secret, 400},
		{"oversized", strings.Repeat("x", (2<<20)+1), secret, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			request := signedPullRequest(tc.body, tc.key)
			if tc.name == "unsigned" {
				request.Header.Del("X-Hub-Signature-256")
			}
			server.ServeHTTP(w, request)
			if w.Code != tc.code {
				t.Fatalf("response %d, want %d", w.Code, tc.code)
			}
			if len(s.posts) != 1 {
				t.Fatal("unauthenticated event changed status")
			}
		})
	}
	s.fail = true
	w := httptest.NewRecorder()
	server.ServeHTTP(w, signedPullRequest(raw, secret))
	if w.Code != 502 {
		t.Fatalf("failed post response %d", w.Code)
	}
	s.f.tx(t, func(tx pgx.Tx) error {
		got, err := load(t.Context(), tx, v.OrderID)
		if err == nil && (got.GateOpen || got.GitHubStatus != "stale") {
			t.Error("failed revocation did not close local gate")
		}
		var previous string
		if err == nil {
			err = tx.QueryRow(t.Context(), `SELECT github_reported_state FROM work_order_reviews WHERE work_order_id=$1`, v.OrderID).Scan(&previous)
		}
		if err == nil && previous != "success" {
			t.Error("failed revocation erased last posted state")
		}
		return err
	})
	// Even though the live base has returned to the reviewed SHA, the
	// authenticated invalidation remains irrevocable and gets retried.
	s.fail = false
	s.f.m.reportStatusLane(t.Context(), true)
	if len(s.posts) != 2 || s.posts[1] != "error" {
		t.Fatalf("failed signal did not retry: %v", s.posts)
	}
	w = httptest.NewRecorder()
	server.ServeHTTP(w, signedPullRequest(raw, secret))
	if w.Code != 204 || len(s.posts) != 2 {
		t.Fatal("replay changed an already revoked review")
	}
	conn, err := s.f.m.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// A reporter may have loaded this green snapshot before the webhook.
	err = s.f.m.publishReview(t.Context(), conn, s.f.person.TenantID, v, nil)
	conn.Release()
	if err != nil || len(s.posts) != 2 {
		t.Fatal("old in-flight snapshot revived a revoked status")
	}
	s.f.m.ConfigureWebhook(nil)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, signedPullRequest(raw, secret))
	if w.Code != 404 {
		t.Fatal("unconfigured webhook was available")
	}
}

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
		if _, err := agentruns.QueueReview(t.Context(), tx, f.person, o, f.agent.ID, profile, f.account, nil, "any", nil); err == nil {
			t.Error("QueueReview trusted a legacy family label")
		}
		return nil
	})
}
