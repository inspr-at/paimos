// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

const pendingVersion = "261003090000.0.0"

func pendingHash(n int) string { return fmt.Sprintf("%040x", n) }

func pendingFixtureCommit(n int, message string, parents int) pendingCommit {
	c := pendingCommit{SHA: pendingHash(n)}
	c.Commit.Message = message
	// Intentionally older than the live release. Reachability, not an author
	// date filter, decides whether this commit belongs in pending changes.
	c.Commit.Author.Date = time.Date(2010, 1, 1, 0, 0, n, 0, time.UTC)
	for i := 0; i < parents; i++ {
		c.Parents = append(c.Parents, struct {
			SHA string `json:"sha"`
		}{SHA: pendingHash(n - i - 1)})
	}
	return c
}

func pendingFixture(t *testing.T, commits []pendingCommit, total int, status string) (*Module, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("public source query unexpectedly sent credentials")
		}
		switch r.URL.Path {
		case "/repos/inspr-at/paimos/git/ref/heads/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": pendingHash(999)}})
		case "/repos/inspr-at/paimos/compare/" + pendingHash(100) + "..." + pendingHash(999):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "total_commits": total, "commits": commits, "base_commit": map[string]string{"sha": pendingHash(100)}})
		default:
			t.Errorf("unexpected source path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	m := NewWith(History{Repository: "inspr-at/paimos", Releases: []Release{
		// A newer tagged release is deliberately present: the baseline must be
		// the running version even when this server has rolled back.
		{Version: "261003100000.0.0", State: StatePublished, Evidence: Evidence{SourceCommit: pendingHash(200)}},
		{Version: pendingVersion, State: StatePublished, Evidence: Evidence{SourceCommit: pendingHash(100)}},
	}}, pendingVersion)
	m.pending.github.API = upstream.URL
	m.pending.github.Client = upstream.Client()
	return m, requests
}

func pendingCall(t *testing.T, m *Module, principal *tenant.Principal, query string) (*httptest.ResponseRecorder, PendingReleaseChanges) {
	t.Helper()
	mux := http.NewServeMux()
	m.Mount(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/releases/pending"+query, nil)
	if principal != nil {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), *principal))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var result PendingReleaseChanges
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode pending response: %v", err)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("pending response is cacheable across principals")
		}
	}
	return w, result
}

func TestPendingChangesRunningSourcePaginationAndTenantClassification(t *testing.T) {
	commits := []pendingCommit{
		pendingFixtureCommit(101, "fix(AEON-101): old authored patch", 1),
		pendingFixtureCommit(102, "feat(AEON-102): shared feature", 1),
		pendingFixtureCommit(103, "AEON-635: release face", 1),
		pendingFixtureCommit(104, "Merge feature branch", 2),
		pendingFixtureCommit(105, "release: v261003100000.0.0", 1),
		pendingFixtureCommit(106, "chore: prepared metadata", 1),
	}
	m, requests := pendingFixture(t, commits, len(commits), "ahead")
	p := tenant.Principal{ID: "person-a", TenantID: "tenant-a", Kind: tenant.Person}
	m.UseTickets(func(ctx context.Context, tenantID string, keys []string) (map[string]TicketMeta, error) {
		if caller, _ := tenant.PrincipalFrom(ctx); caller.TenantID != tenantID {
			t.Errorf("classification tenant %s differs from authenticated caller %s", tenantID, caller.TenantID)
		}
		if len(keys) > maxPendingKeys {
			t.Fatal("unbounded ticket classification")
		}
		if tenantID == "tenant-a" {
			return map[string]TicketMeta{"AEON-635": {Bug: true, Note: &TicketNote{PillEN: "private current copy"}}}, nil
		}
		return map[string]TicketMeta{}, nil
	})
	w, first := pendingCall(t, m, &p, "?limit=2")
	if w.Code != 200 || first.Status != "available" || first.Total == nil || *first.Total != 4 || first.KnownTotal != 4 || len(first.Changes) != 2 || first.NextCursor == nil {
		t.Fatalf("first page: %d %+v", w.Code, first)
	}
	if first.LiveVersion != pendingVersion || first.BaseCommit != pendingHash(100) || first.HeadCommit != pendingHash(999) || first.Changes[0].Commit != pendingHash(106) || first.Changes[1].Group != GroupFixes {
		t.Fatalf("wrong live baseline/order/group: %+v", first)
	}
	if strings.Contains(w.Body.String(), "private current copy") || len(first.Changes[1].Linked) != 0 {
		t.Fatal("pending response copied live tenant benefit text")
	}
	w, second := pendingCall(t, m, &p, "?limit=2&cursor="+*first.NextCursor)
	if w.Code != 200 || len(second.Changes) != 2 || second.NextCursor != nil || second.Changes[0].Commit != pendingHash(102) || second.Changes[1].Commit != pendingHash(101) {
		t.Fatalf("second page: %d %+v", w.Code, second)
	}
	for _, change := range append(first.Changes, second.Changes...) {
		if change.Commit == pendingHash(100) || change.Type == "release" || change.Commit == pendingHash(104) {
			t.Fatalf("released/merge/bump commit counted: %+v", change)
		}
	}
	p.TenantID = "tenant-b"
	w, other := pendingCall(t, m, &p, "?limit=2")
	if w.Code != 200 || other.Changes[1].Group != GroupOther || requests.Load() != 2 {
		t.Fatalf("shared cache leaked classification or fetched again: %d %+v requests=%d", w.Code, other, requests.Load())
	}
	// Identical cursor state works for another tenant without copying tenant-A
	// metadata; all cursor data names public commits only.
	w, _ = pendingCall(t, m, &p, "?cursor="+*first.NextCursor)
	if w.Code != 200 {
		t.Fatalf("public continuation rejected in another tenant: %d", w.Code)
	}
}

func TestPendingChangesAuthenticationAndBoundsBeforeUpstream(t *testing.T) {
	m, requests := pendingFixture(t, nil, 0, "identical")
	person := tenant.Principal{ID: "person", TenantID: "tenant", Kind: tenant.Person}
	for _, tc := range []struct {
		name      string
		principal *tenant.Principal
		query     string
		want      int
	}{
		{"anonymous", nil, "", 401},
		{"missing identity", &tenant.Principal{TenantID: "tenant"}, "", 401},
		{"missing tenant", &tenant.Principal{ID: "person"}, "", 401},
		{"zero limit", &person, "?limit=0", 400},
		{"large limit", &person, "?limit=101", 400},
		{"negative limit", &person, "?limit=-1", 400},
		{"nonnumeric limit", &person, "?limit=all", 400},
		{"duplicate limit", &person, "?limit=1&limit=2", 400},
		{"malformed cursor", &person, "?cursor=not-a-cursor", 400},
		{"large cursor", &person, "?cursor=" + strings.Repeat("a", 257), 400},
		{"large query", &person, "?x=" + strings.Repeat("a", 1025), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := pendingCall(t, m, tc.principal, tc.query)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid/unauthenticated requests touched upstream: %d", requests.Load())
	}
	if permission, ok := authz.PermissionForPattern("GET /api/releases/pending"); !ok || permission != "releases.read" {
		t.Fatalf("pending route differs from release-sheet permission: %q %v", permission, ok)
	}
	w, result := pendingCall(t, m, &person, "?limit=100")
	if w.Code != 200 || result.Total == nil || *result.Total != 0 || result.Status != "available" {
		t.Fatalf("confirmed empty comparison: %d %+v", w.Code, result)
	}
}

func TestPendingChangesPartialAndUnavailableEvidence(t *testing.T) {
	person := tenant.Principal{ID: "person", TenantID: "tenant", Kind: tenant.Person}
	for _, tc := range []struct {
		name, state string
		commits     []pendingCommit
		total       int
		want        string
		known       int
	}{
		{"truncated", "ahead", []pendingCommit{pendingFixtureCommit(101, "fix: patch", 1)}, 251, "partial", 1},
		{"diverged", "diverged", []pendingCommit{pendingFixtureCommit(101, "fix: patch", 1)}, 1, "unavailable", 0},
		{"behind", "behind", nil, 0, "unavailable", 0},
		{"base erroneously included", "ahead", []pendingCommit{pendingFixtureCommit(100, "feat: already live", 1)}, 1, "unavailable", 0},
		{"incomplete commit", "ahead", []pendingCommit{{SHA: pendingHash(101)}}, 1, "unavailable", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := pendingFixture(t, tc.commits, tc.total, tc.state)
			w, result := pendingCall(t, m, &person, "")
			if w.Code != 200 || result.Status != tc.want || result.Total != nil || result.KnownTotal != tc.known || len(result.Unavailable) == 0 {
				t.Fatalf("evidence presented as success/zero: %d %+v", w.Code, result)
			}
		})
	}
	m, requests := pendingFixture(t, nil, 0, "identical")
	m.current = "dev"
	w, result := pendingCall(t, m, &person, "")
	if w.Code != 200 || result.Status != "unavailable" || result.Total != nil || requests.Load() != 0 {
		t.Fatalf("missing live source fabricated count/fetched upstream: %d %+v requests=%d", w.Code, result, requests.Load())
	}
}

func TestPendingChangesCursorIdentityAndCacheExpiry(t *testing.T) {
	m, requests := pendingFixture(t, []pendingCommit{pendingFixtureCommit(101, "fix: first", 1), pendingFixtureCommit(102, "feat: second", 1)}, 2, "ahead")
	person := tenant.Principal{ID: "person", TenantID: "tenant", Kind: tenant.Person}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	m.pending.now = func() time.Time { return now }
	_, first := pendingCall(t, m, &person, "?limit=1")
	if first.NextCursor == nil {
		t.Fatal("fixture did not exercise continuation")
	}
	wrongHead := base64.RawURLEncoding.EncodeToString([]byte(pendingHash(888) + ":" + pendingHash(102)))
	w, _ := pendingCall(t, m, &person, "?cursor="+wrongHead)
	if w.Code != 409 {
		t.Fatalf("changed snapshot accepted: %d %s", w.Code, w.Body.String())
	}
	wrongKey := base64.RawURLEncoding.EncodeToString([]byte(pendingHash(999) + ":" + pendingHash(100)))
	w, _ = pendingCall(t, m, &person, "?cursor="+wrongKey)
	if w.Code != 400 {
		t.Fatalf("already-live commit accepted as continuation: %d", w.Code)
	}
	now = now.Add(pendingTTL)
	w, refreshed := pendingCall(t, m, &person, "?cursor="+*first.NextCursor)
	if w.Code != 200 || !refreshed.CheckedAt.Equal(now) || requests.Load() != 4 || len(refreshed.Changes) != 1 || refreshed.Changes[0].Commit != pendingHash(101) {
		t.Fatalf("cache refresh lost pinned continuation: %d %+v requests=%d", w.Code, refreshed, requests.Load())
	}
}

type pendingRoundTripper func(*http.Request) (*http.Response, error)

func (f pendingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPendingLookupResponseSizeStatusAndDeadlineBounds(t *testing.T) {
	person := tenant.Principal{ID: "person", TenantID: "tenant", Kind: tenant.Person}
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"rate limited", http.StatusTooManyRequests, `{}`},
		{"invalid json", http.StatusOK, `{`},
		{"oversized main ref", http.StatusOK, strings.Repeat(" ", (64<<10)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := pendingFixture(t, nil, 0, "identical")
			m.pending.github.Client = &http.Client{Transport: pendingRoundTripper(func(r *http.Request) (*http.Response, error) {
				if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 6*time.Second {
					t.Fatal("upstream request has no six-second deadline")
				}
				return &http.Response{StatusCode: tc.code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			w, result := pendingCall(t, m, &person, "")
			if w.Code != 200 || result.Status != "unavailable" || result.Total != nil || len(result.Unavailable) == 0 {
				t.Fatalf("upstream failure fabricated success: %d %+v", w.Code, result)
			}
		})
	}
	m, _ := pendingFixture(t, nil, 0, "identical")
	m.pending.github.Client = &http.Client{Transport: pendingRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/git/ref/") {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"object":{"sha":"` + pendingHash(999) + `"}}`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat(" ", maxPendingBytes+1)))}, nil
	})}
	w, result := pendingCall(t, m, &person, "")
	if w.Code != 200 || result.Status != "unavailable" || result.Total != nil {
		t.Fatalf("oversized comparison accepted: %d %+v", w.Code, result)
	}
	// Cancellation is established directly, with no sleeps or elapsed-time
	// assertion. Even a request waiting behind the fetch gate terminates.
	s := newPendingSource()
	s.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result = s.snapshot(ctx, "inspr-at/paimos", pendingVersion, pendingHash(100))
	if result.Status != "unavailable" || result.Total != nil {
		t.Fatalf("cancelled fetch gate returned success: %+v", result)
	}
}

func TestPendingClassificationFailureRemainsExplicit(t *testing.T) {
	m, _ := pendingFixture(t, []pendingCommit{pendingFixtureCommit(101, "AEON-635: release face", 1)}, 1, "ahead")
	m.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		return nil, errors.New("classification unavailable")
	})
	person := tenant.Principal{ID: "person", TenantID: "tenant", Kind: tenant.Person}
	w, result := pendingCall(t, m, &person, "")
	if w.Code != 200 || result.Status != "partial" || result.Total != nil || result.KnownTotal != 1 || len(result.Changes) != 1 || result.Changes[0].Group != GroupOther || len(result.Unavailable) != 1 {
		t.Fatalf("classification failure hidden: %d %+v", w.Code, result)
	}
}

func TestPendingTicketKeyFanoutIsBoundedAndExplicit(t *testing.T) {
	commits := []pendingCommit{}
	for i := 0; i < 100; i++ {
		keys := []string{}
		for j := 0; j < 11; j++ {
			keys = append(keys, fmt.Sprintf("AEON-%d", 1000+i*11+j))
		}
		commits = append(commits, pendingFixtureCommit(101+i, strings.Join(keys, " ")+": merged changes", 1))
	}
	m, _ := pendingFixture(t, commits, len(commits), "ahead")
	lookedUp := 0
	m.UseTickets(func(_ context.Context, _ string, keys []string) (map[string]TicketMeta, error) {
		lookedUp = len(keys)
		return map[string]TicketMeta{}, nil
	})
	person := tenant.Principal{ID: "person", TenantID: "tenant", Kind: tenant.Person}
	w, result := pendingCall(t, m, &person, "?limit=100")
	if w.Code != 200 || lookedUp != maxPendingKeys || result.Status != "partial" || result.Total != nil || result.KnownTotal != 100 || len(result.Changes) != 100 || len(result.Unavailable) != 1 {
		t.Fatalf("fanout bound or partial notice missing: %d lookedUp=%d %+v", w.Code, lookedUp, result)
	}
}
