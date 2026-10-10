// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/jackc/pgx/v5/pgxpool"
)

const webhookPrivateText = "private-ticket-body-do-not-log"

type webhookLogGitHub struct {
	*fakeGitHub
	err   error
	facts []MergeFact
	reads int
}

func (g *webhookLogGitHub) MergedPull(context.Context, int64) (*MergeFact, error) {
	g.reads++
	if len(g.facts) > 0 {
		return &g.facts[0], g.err
	}
	return nil, g.err
}
func (g *webhookLogGitHub) MergedGroup(context.Context, string) ([]MergeFact, error) {
	g.reads++
	return g.facts, g.err
}
func (*webhookLogGitHub) AuditCommits(context.Context) ([]string, error) { return nil, nil }
func (*webhookLogGitHub) AuditCommit(context.Context, string) (*MergeFact, error) {
	return nil, nil
}
func (*webhookLogGitHub) AuditChecks(context.Context, string) ([]Check, error) { return nil, nil }

func webhookLogFixture() (*Module, *webhookLogGitHub, *bytes.Buffer) {
	g := &webhookLogGitHub{fakeGitHub: &fakeGitHub{}}
	m := New(nil, crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: "00000000-0000-4000-8000-000000000001", Repository: "example/delivery", KeyFile: "/unused/log-fixture.pem"}, []byte(strings.Repeat("s", 32)), g, nil)
	m.now = func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }
	var logs bytes.Buffer
	m.webhookFailures.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	return m, g, &logs
}

func webhookLogRequest(event, raw string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/github/webhook", strings.NewReader(raw))
	r.Header.Set("X-GitHub-Event", event)
	r.Header.Set("X-GitHub-Delivery", "delivery-log-test")
	return r
}

func assertWebhookFailureLine(t *testing.T, logs *bytes.Buffer, event, action, step, class string, status int, extra map[string]any) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("wanted exactly one structured line, got %d", len(lines))
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"event": event, "action": action, "delivery_id": "delivery-log-test", "step": step, "status": float64(status), "error_class": class, "suppressed": float64(0), "level": "WARN", "msg": "GitHub webhook delivery failed"}
	for key, value := range extra {
		want[key] = value
	}
	if _, ok := fields["time"].(string); !ok {
		t.Fatal("missing structured log time")
	}
	delete(fields, "time")
	if len(fields) != len(want) {
		t.Fatalf("unexpected log fields: %v", fields)
	}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("%s = %v, want %v", key, fields[key], value)
		}
	}
	for _, private := range []string{webhookPrivateText, "sha256=", "Authorization", "payload", `"signature":`, "token", "pull_request.title"} {
		if strings.Contains(logs.String(), private) {
			t.Fatalf("private field leaked: %s", private)
		}
	}
}

func TestWebhookFailureParse(t *testing.T) {
	// Risk: rejected body/JSON content leaks while a parse failure stays silent.
	t.Run("accepted envelope parse", func(t *testing.T) {
		m, g, logs := webhookLogFixture()
		raw := `{"action":"closed","pull_request":{"number":"` + webhookPrivateText + `"}}`
		r := webhookLogRequest("pull_request", raw)
		w := httptest.NewRecorder()
		m.finishMergeWebhook(w, r, []byte(raw), g)
		if w.Code != 400 || g.reads != 0 {
			t.Fatalf("parse did not refuse before a reader: %d, reads %d", w.Code, g.reads)
		}
		assertWebhookFailureLine(t, logs, "pull_request", "closed", "parse", "invalid_json", 400, nil)
	})
	t.Run("body limit", func(t *testing.T) {
		m, _, logs := webhookLogFixture()
		r := webhookLogRequest("pull_request", strings.Repeat("x", (2<<20)+1))
		w := httptest.NewRecorder()
		m.mergeWebhook(w, r)
		if w.Code != 413 {
			t.Fatalf("body limit status: %d", w.Code)
		}
		assertWebhookFailureLine(t, logs, "pull_request", "unknown", "parse", "body_too_large", 413, nil)
	})
}

func TestWebhookFailureAudit(t *testing.T) {
	// Risk: nested ingress/audit handlers emit no line or several lines for a
	// refusal, or an unauthenticated action injects payload text into logs.
	base := `"installation":{"id":456},"repository":{"full_name":"example/delivery"}`
	for _, tc := range []struct {
		name, raw, action, class string
		status                   int
		sign, fallback           bool
	}{
		{"signature", `{"action":"completed",` + base + `,"body":"` + webhookPrivateText + `"}`, "completed", "invalid_signature", 401, false, false},
		{"fallback ingress", `{"action":"completed",` + base + `}`, "completed", "invalid_signature", 401, false, true},
		{"invalid action", `{"action":"` + webhookPrivateText + `",` + base + `}`, "unknown", "invalid_envelope", 400, true, false},
		{"malformed JSON", `{"action":"completed","body":"` + webhookPrivateText + `"`, "unknown", "invalid_envelope", 400, true, false},
		{"installation", `{"action":"completed","installation":{"id":999},"repository":{"full_name":"example/delivery"}}`, "completed", "installation_mismatch", 404, true, false},
		{"audit parse", `{"action":"completed",` + base + `,"pull_request":{"merged_at":"` + webhookPrivateText + `"}}`, "completed", "invalid_json", 400, true, false},
		{"metrics error", `{"action":"completed",` + base + `,"created_at":"` + webhookPrivateText + `"}`, "completed", "observation_unavailable", 502, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, g, logs := webhookLogFixture()
			if tc.fallback {
				m.github = g.fakeGitHub
			}
			r := webhookLogRequest("workflow_run", tc.raw)
			if tc.sign {
				r.Header.Set("X-Hub-Signature-256", signed([]byte(tc.raw), m.secret))
			}
			w := httptest.NewRecorder()
			m.mergeWebhook(w, r)
			if w.Code != tc.status {
				t.Fatalf("audit status: %d, want %d", w.Code, tc.status)
			}
			assertWebhookFailureLine(t, logs, "workflow_run", tc.action, "audit", tc.class, tc.status, nil)
		})
	}
}

func TestWebhookFailureMergedPull(t *testing.T) {
	// Risk: canonical pull-reader failures disappear or log the error's body.
	m, g, logs := webhookLogFixture()
	g.err = errors.New(webhookPrivateText)
	raw := `{"action":"closed","pull_request":{"number":7,"title":"` + webhookPrivateText + `"}}`
	w := httptest.NewRecorder()
	m.finishMergeWebhook(w, webhookLogRequest("pull_request", raw), []byte(raw), g)
	if w.Code != 502 || g.reads != 1 {
		t.Fatalf("pull-reader failure: %d, reads %d", w.Code, g.reads)
	}
	assertWebhookFailureLine(t, logs, "pull_request", "closed", "reader-MergedPull", "internal", 502, nil)
}

func TestWebhookFailureCompletedGroup(t *testing.T) {
	// Risk: the constituent-ledger read fails silently before a canonical read.
	m, g, logs := webhookLogFixture()
	pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m.pool = pool
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // No connection is attempted; fail the actual ledger operation.
	raw := `{"action":"destroyed","merge_group":{"head_sha":"` + strings.Repeat("a", 40) + `"},"body":"` + webhookPrivateText + `"}`
	w := httptest.NewRecorder()
	m.finishMergeWebhook(w, webhookLogRequest("merge_group", raw).WithContext(ctx), []byte(raw), g)
	if w.Code != 502 || g.reads != 0 {
		t.Fatalf("group ledger did not fail before reader: %d, reads %d", w.Code, g.reads)
	}
	assertWebhookFailureLine(t, logs, "merge_group", "destroyed", "reader-completedGroup", "canceled", 502, nil)
}

func TestWebhookFailureMergedGroup(t *testing.T) {
	// Risk: a push's canonical group-reader failure is misattributed to completion.
	m, g, logs := webhookLogFixture()
	g.err = context.DeadlineExceeded
	raw := `{"ref":"refs/heads/main","after":"` + strings.Repeat("a", 40) + `","body":"` + webhookPrivateText + `"}`
	w := httptest.NewRecorder()
	m.finishMergeWebhook(w, webhookLogRequest("push", raw), []byte(raw), g)
	if w.Code != 502 || g.reads != 1 {
		t.Fatalf("group-reader failure: %d, reads %d", w.Code, g.reads)
	}
	assertWebhookFailureLine(t, logs, "push", "", "reader-MergedGroup", "deadline_exceeded", 502, nil)
}

func TestWebhookFailureCompleteMerges(t *testing.T) {
	// Risk: completion errors are mislabeled as a successful reader operation.
	m, g, logs := webhookLogFixture()
	g.facts = make([]MergeFact, 101) // The real completion's bounded-input refusal.
	for i := range g.facts {
		g.facts[i].Title = webhookPrivateText
	}
	raw := `{"ref":"refs/heads/main","after":"` + strings.Repeat("a", 40) + `"}`
	w := httptest.NewRecorder()
	m.finishMergeWebhook(w, webhookLogRequest("push", raw), []byte(raw), g)
	if w.Code != 502 || g.reads != 1 {
		t.Fatalf("completion failure: %d, reads %d", w.Code, g.reads)
	}
	assertWebhookFailureLine(t, logs, "push", "", "completeMerges", "observation_unavailable", 502, nil)
}

func TestWebhookFailureRateLimit(t *testing.T) {
	// Risk: concurrent redeliveries flood logs, or per-delivery keys grow memory.
	m, g, logs := webhookLogFixture()
	g.err = errors.New(webhookPrivateText)
	raw := `{"action":"closed","pull_request":{"number":7}}`
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		workers.Go(func() {
			<-start
			w := httptest.NewRecorder()
			m.rejectWebhook(w, webhookLogRequest("pull_request", raw), "closed", "reader-MergedPull", 502, g.err)
			if w.Code != 502 {
				t.Error("suppression changed the response")
			}
		})
	}
	close(start)
	workers.Wait()
	assertWebhookFailureLine(t, logs, "pull_request", "closed", "reader-MergedPull", "internal", 502, nil)
	logs.Reset()
	at := m.now()
	m.now = func() time.Time { return at.Add(time.Minute - time.Nanosecond) }
	m.rejectWebhook(httptest.NewRecorder(), webhookLogRequest("pull_request", raw), "closed", "reader-MergedPull", 502, g.err)
	if logs.Len() != 0 {
		t.Fatal("emitted before the minute boundary")
	}
	m.now = func() time.Time { return at.Add(time.Minute) }
	m.rejectWebhook(httptest.NewRecorder(), webhookLogRequest("pull_request", raw), "closed", "reader-MergedPull", 502, g.err)
	assertWebhookFailureLine(t, logs, "pull_request", "closed", "reader-MergedPull", "internal", 502, map[string]any{"suppressed": float64(50)})
	logs.Reset()
	m.rejectWebhook(httptest.NewRecorder(), webhookLogRequest("pull_request", raw), "closed", "reader-MergedPull", 502, context.Canceled)
	assertWebhookFailureLine(t, logs, "pull_request", "closed", "reader-MergedPull", "canceled", 502, nil)
	logs.Reset()
	m.rejectWebhook(httptest.NewRecorder(), webhookLogRequest("pull_request", raw), "closed", "audit", 502, g.err)
	assertWebhookFailureLine(t, logs, "pull_request", "closed", "audit", "internal", 502, nil)
	if len(m.webhookFailures.windows) != 3 {
		t.Fatal("limiter includes more than step/class keys")
	}
}

func TestWebhookFailureGitHubMetadata(t *testing.T) {
	// Risk: GitHub status/request id are lost at mint/read boundaries, 404 loses
	// its absence semantics, or upstream bodies/credentials enter a log line.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "fixture.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, id string
		status   int
		mint     bool
	}{
		{"mint forbidden", "A1:B2-C3", 403, true},
		{"read unavailable", "A1:B2-C3", 503, false},
		{"read missing", "A1:B2-C3", 404, false},
		{"malformed success", "A1:B2-C3", 200, false},
		{"absent request id", "", 429, false},
		{"unsafe request id", webhookPrivateText + "\n", 502, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, logs := webhookLogFixture()
			config := m.config
			config.KeyFile = keyFile
			app := &crossreview.GitHubApp{Config: config, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				status, body := tc.status, webhookPrivateText
				if r.Method == http.MethodDelete {
					status, body = 204, ""
				} else if r.Method == http.MethodPost && !tc.mint {
					status = 201
					var request struct {
						Permissions map[string]string `json:"permissions"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					if len(request.Permissions) != 5 {
						t.Fatal("changed read authority")
					}
					for _, level := range request.Permissions {
						if level != "read" {
							t.Fatal("write permission requested")
						}
					}
					raw, _ := json.Marshal(map[string]any{"token": "ephemeral-fixture-token", "permissions": request.Permissions, "repositories": []any{map[string]string{"full_name": config.Repository}}})
					body = string(raw)
				}
				return &http.Response{StatusCode: status, Header: http.Header{"X-Github-Request-Id": []string{tc.id}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			reader := AppReader{App: app}
			_, readErr := reader.MergedPull(t.Context(), 7)
			if readErr == nil || errors.Is(readErr, crossreview.ErrNotFound) != (tc.status == 404) || errors.Is(readErr, crossreview.ErrUnavailable) != (tc.status != 404) {
				t.Fatal("HTTP error changed absence/refusal semantics")
			}
			var metadata *crossreview.GitHubHTTPError
			if !errors.As(readErr, &metadata) || metadata.StatusCode != tc.status || strings.Contains(readErr.Error(), webhookPrivateText) {
				t.Fatal("HTTP metadata lost or error contains body")
			}
			raw := `{"action":"closed","pull_request":{"number":7,"title":"` + webhookPrivateText + `"}}`
			w := httptest.NewRecorder()
			m.finishMergeWebhook(w, webhookLogRequest("pull_request", raw), []byte(raw), reader)
			if w.Code != 502 {
				t.Fatalf("reader status: %d", w.Code)
			}
			extra := map[string]any{"github_status": float64(tc.status)}
			if tc.id == "A1:B2-C3" {
				extra["github_request_id"] = tc.id
			}
			assertWebhookFailureLine(t, logs, "pull_request", "closed", "reader-MergedPull", "github_http", 502, extra)
		})
	}
}

func TestWebhookFailureSuccessIsQuiet(t *testing.T) {
	// Risk: diagnostics create failure lines for successful/no-op deliveries.
	m, g, logs := webhookLogFixture()
	raw := `{"action":"closed","pull_request":{"number":7}}`
	w := httptest.NewRecorder()
	m.finishMergeWebhook(w, webhookLogRequest("pull_request", raw), []byte(raw), g)
	if w.Code != 204 || logs.Len() != 0 || g.reads != 1 {
		t.Fatalf("success: status %d, log bytes %d, reads %d", w.Code, logs.Len(), g.reads)
	}
}
