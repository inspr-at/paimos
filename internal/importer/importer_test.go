// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func fakeClassic(t *testing.T) (*HTTPSource, func()) {
	t.Helper()
	responses := map[string]string{
		"/api/projects?status=all":       `[{"id":3,"key":"PAI","name":"Paimos","description":"legacy","status":"active","rate_hourly":100,"tags":["studio"],"customer_label":"Client","product_owner":7,"ai_defaults":{"mode":"manual"},"active_issue_count":2}]`,
		"/api/projects?status=deleted":   `[]`,
		"/api/users":                     `[{"id":7,"username":"markus","email":"markus@example.test","role":"admin","locale":"de"}]`,
		"/api/users?status=deleted":      `[]`,
		"/api/issues?limit=100&offset=0": `{"issues":[{"id":99,"issue_key":"SPRINT-99","type":"sprint","title":"Sprint","status":"open"}],"has_more":false}`,
		"/api/issues/trash":              `[]`,
		"/api/projects/3/issues":         `[{"id":10,"project_id":3,"issue_key":"PAI-10","type":"epic","title":"Epic","status":"open","description":"root"},{"id":11,"project_id":3,"issue_key":"PAI-11","type":"ticket","title":"Ticket","status":"open","description":"body","parent_id":10,"priority":"high","acceptance_criteria":"- [ ] done","notes":"**private**","tags":["ready"],"assignee_id":7,"created_by":7,"estimate_hours":2.5,"estimate_lp":3,"budget_hours":8,"total_budget":1000,"start_date":"2026-01-02","end_date":"2026-01-09","release":"r1","sprint_ids":[99],"needs_review":true,"archived":false,"accepted_at":"2026-01-10T10:00:00Z","accepted_by":7,"jira_text":"verbatim"}]`,
		"/api/projects/3/knowledge":      `[{"id":12,"project_id":3,"type":"memory","slug":"lesson","title":"Lesson","body":"keep","status":"active","metadata":{"a":1}}]`,
		"/api/issues/12":                 `{"id":12,"project_id":3,"issue_key":"PAI-12","type":"memory","title":"Lesson","status":"active"}`,
		"/api/issues/10/relations":       `[{"source_id":10,"target_id":11,"type":"parent"}]`,
		"/api/issues/11/relations":       `[{"source_id":11,"target_id":10,"type":"depends_on"}]`,
		"/api/issues/11/comments":        `[{"id":30,"issue_id":11,"body":"comment","visibility":"internal","created_at":"2026-01-01 12:00:00"}]`,
		"/api/issues/11/history":         `[{"id":40,"issue_id":11,"snapshot":{"status":"open"},"changed_at":"2026-01-01 12:00:00"}]`,
		"/api/issues/11/attachments":     `[{"id":50,"issue_id":11,"object_key":"private/object","filename":"a.txt","size_bytes":4,"created_at":"2026-01-01 12:00:00"}]`,
		"/api/attachments/50":            `data`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("source method %s", r.Method)
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fake-key" {
			t.Error("missing bearer")
			w.WriteHeader(401)
			return
		}
		body, ok := responses[r.URL.RequestURI()]
		if !ok && strings.HasSuffix(r.URL.Path, "/relations") {
			body = "[]"
			ok = true
		}
		if !ok && strings.HasSuffix(r.URL.Path, "/comments") {
			body = "[]"
			ok = true
		}
		if !ok && strings.HasSuffix(r.URL.Path, "/history") {
			body = "[]"
			ok = true
		}
		if !ok && strings.HasSuffix(r.URL.Path, "/attachments") {
			body = "[]"
			ok = true
		}
		if !ok {
			t.Errorf("unexpected source GET %s", r.URL.RequestURI())
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	file := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(file, []byte("fake-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := NewHTTPSource(server.URL, file, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return src, server.Close
}

func TestImportDryRunAndRerun(t *testing.T) {
	source, closeServer := fakeClassic(t)
	defer closeServer()
	ctx := context.Background()
	dry, err := (Importer{Source: source}).Run(ctx, "test", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Counts["projects"] != 1 || dry.Counts["ticket"] != 1 || dry.Counts["memory"] != 1 || dry.Counts["sprint"] != 1 || dry.Counts["comments"] != 1 {
		t.Fatalf("wrong dry run: %+v", dry)
	}
	if len(dry.UnmappedFields) != 0 {
		t.Fatalf("work fields remain unmapped: %+v", dry.UnmappedFields)
	}
	d := dbtest.Open(t)
	if err := db.EnsureTenant(ctx, d.Admin, "test", "Test"); err != nil {
		t.Fatal(err)
	}
	job := Importer{Source: source, Writer: PostgresWriter{Pool: d.App}}
	first, err := job.Run(ctx, "test", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Created != 5 {
		t.Fatalf("created %d", first.Created)
	}
	var plannedRows int
	if err := d.Admin.QueryRow(ctx, `SELECT reltuples::int FROM pg_class WHERE oid='nodes'::regclass`).Scan(&plannedRows); err != nil {
		t.Fatal(err)
	}
	if plannedRows < first.Created {
		t.Fatalf("nodes statistics were not refreshed after import: estimated %d rows, created %d", plannedRows, first.Created)
	}
	second, err := job.Run(ctx, "test", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created != 0 || second.Updated != 0 {
		t.Fatalf("rerun changed nodes: %+v", second)
	}
	var count int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE key IN ('PAI-10','PAI-11','PAI-12','SPRINT-99')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("issue keys lost: %d", count)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM nodes child JOIN nodes parent ON child.parent_id=parent.id WHERE child.key='PAI-11' AND parent.key='PAI-10'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("parent edge missing")
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM node_relations r JOIN nodes source ON r.source_node_id=source.id JOIN nodes target ON r.target_node_id=target.id WHERE r.type='blocks' AND source.key='PAI-10' AND target.key='PAI-11'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("dependency direction lost")
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE type IN ('import.comment','import.history','import.attachment')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("auxiliary events duplicated or lost: %d", count)
	}
	var commentAt time.Time
	if err := d.Admin.QueryRow(ctx, `SELECT at FROM events WHERE type='import.comment'`).Scan(&commentAt); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC); !commentAt.Equal(want) {
		t.Fatalf("classic comment time: got %s, want %s", commentAt, want)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='import.parent_changed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("parent events duplicated or lost: %d", count)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM identities WHERE issuer='paimos-classic'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("identities: %d", count)
	}
	var fields []byte
	if err := d.Admin.QueryRow(ctx, `SELECT fields FROM nodes WHERE key='PAI-11'`).Scan(&fields); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(fields, &data); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"acceptance_criteria": "- [ ] done", "notes": "**private**", "priority": "high",
		"estimate_hours": 2.5, "estimate_lp": float64(3), "budget_hours": float64(8),
		"total_budget": float64(1000), "start_date": "2026-01-02", "end_date": "2026-01-09",
		"release": "r1", "needs_review": true, "archived": false,
		"accepted_at": "2026-01-10T10:00:00Z",
	} {
		if data[key] != want {
			t.Errorf("%s = %v, want %v", key, data[key], want)
		}
	}
	if data["tags"].([]any)[0] != "ready" || data["sprint_ids"].([]any)[0] != float64(99) {
		t.Fatal("tags or sprint references lost")
	}
	for _, name := range []string{"assignee", "created_by", "accepted_by"} {
		if data[name] == nil || data[name] == "" {
			t.Errorf("%s principal missing", name)
		}
	}
	classic := data["classic"].(map[string]any)
	if classic["jira_text"] != "verbatim" || classic["priority"] != "high" || classic["assignee_id"] != float64(7) {
		t.Fatal("verbatim classic issue fields lost")
	}
	if _, ok := classic["record"]; ok {
		t.Fatal("obsolete classic.record nesting")
	}
	if err := d.Admin.QueryRow(ctx, `SELECT fields FROM nodes WHERE key='PRJ-3'`).Scan(&fields); err != nil {
		t.Fatal(err)
	}
	data = nil
	if err := json.Unmarshal(fields, &data); err != nil {
		t.Fatal(err)
	}
	if data["tags"].([]any)[0] != "studio" || data["classic"].(map[string]any)["customer_label"] != "Client" {
		t.Fatal("project fields lost")
	}
	if data["product_owner"] == nil || data["classic"].(map[string]any)["ai_defaults"].(map[string]any)["mode"] != "manual" {
		t.Fatal("project principal or nested classic field lost")
	}
	if _, ok := data["classic"].(map[string]any)["active_issue_count"]; ok {
		t.Fatal("computed project count retained")
	}
}

func TestAllClassicIssueKindsUseR1Nodes(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	if err := db.EnsureTenant(ctx, d.Admin, "kinds", "Kinds"); err != nil {
		t.Fatal(err)
	}
	kinds := []string{"task", "release", "cost_unit", "runbook", "guideline", "external_system", "related_project"}
	p := Project{Record: Record{"id": 1, "key": "PAI", "name": "Paimos", "status": "active"}}
	for n, kind := range kinds {
		p.Issues = append(p.Issues, Record{"id": n + 101, "issue_key": "PAI-" + strconv.Itoa(n+101), "type": kind, "title": kind, "status": "open"})
	}
	snap := Snapshot{SourceID: "test-source", Projects: []Project{p}, Details: map[int64]Details{}}
	result, err := (PostgresWriter{Pool: d.App}).Write(ctx, snap, "kinds")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != len(kinds)+1 {
		t.Fatalf("created %d", result.Created)
	}
	for _, kind := range kinds {
		var count int
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM nodes n JOIN node_kinds k ON n.kind_id=k.id WHERE k.slug=$1`, kind).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("kind %s missing", kind)
		}
	}
}

// Quiescence proves every competing goroutine reached its request or the cap.
// Held transports make saturation deterministic, independent of network timing.
func TestSourceRequestCapAndDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var active, peak atomic.Int32
		entered := make(chan struct{}, 8)
		failures := make(chan error, 8)
		var wg sync.WaitGroup
		release := make(chan struct{})
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
			wg.Wait()
		}()
		base, _ := url.Parse("https://source.example.test")
		source := &HTTPSource{base: base, client: &http.Client{Transport: sourceRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			entered <- struct{}{}
			<-release
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header)}, nil
		})}}
		if err := source.Configure(2, 0); err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		for n := 0; n < 8; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var rows []Record
				failures <- source.get(ctx, "/probe", &rows)
			}()
		}
		synctest.Wait()
		if got := len(entered); got != 2 {
			t.Fatalf("admitted held requests = %d, want 2", got)
		}
		if active.Load() != 2 {
			t.Fatalf("held active requests = %d, want 2", active.Load())
		}
		close(release)
		wg.Wait()
		for n := 0; n < 8; n++ {
			if err := <-failures; err != nil {
				t.Fatal(err)
			}
		}
		if got := peak.Load(); got > 2 {
			t.Fatalf("request cap exceeded: %d", got)
		}
		if len(entered) != 8 {
			t.Fatalf("completed request count = %d, want 8", len(entered))
		}
	})
}

type sourceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sourceRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSourceDispatchDelayUsesInjectedClock(t *testing.T) {
	// synctest injects the standard library clock; the production timer and
	// transport dispatches are exercised without real elapsed-time thresholds.
	synctest.Test(t, func(t *testing.T) {
		origin := time.Now()
		var starts []time.Time
		base, _ := url.Parse("https://source.example.test")
		source := &HTTPSource{base: base, client: &http.Client{Transport: sourceRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			starts = append(starts, time.Now())
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header)}, nil
		})}}
		const delay = 15 * time.Millisecond
		if err := source.Configure(2, delay); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 8; n++ {
			var rows []Record
			if err := source.get(t.Context(), "/probe", &rows); err != nil {
				t.Fatal(err)
			}
		}
		if len(starts) != 8 {
			t.Fatalf("dispatches=%d, want 8", len(starts))
		}
		for n, at := range starts {
			if want := origin.Add(time.Duration(n) * delay); !at.Equal(want) {
				t.Fatalf("dispatch %d=%s, want %s", n, at, want)
			}
		}
	})
}

func TestSourceSkipsDeletedProjectAndPurgedIssues(t *testing.T) {
	routes := map[string]string{
		"/api/projects?status=all":       `[{"id":3,"key":"PAI","name":"Paimos","status":"active"}]`,
		"/api/projects?status=deleted":   `[{"id":4,"key":"OLD","name":"Old","status":"deleted"}]`,
		"/api/users":                     `[]`,
		"/api/users?status=deleted":      `[]`,
		"/api/projects/3/issues":         `[{"id":10,"project_id":3,"issue_key":"PAI-10","type":"ticket","title":"Kept","status":"open"},{"id":11,"project_id":3,"issue_key":"PAI-11","type":"ticket","title":"Purged","status":"open"}]`,
		"/api/projects/3/knowledge":      `[{"id":12,"project_id":3,"type":"memory","title":"Purged knowledge"}]`,
		"/api/issues/10/relations":       `[]`,
		"/api/issues/10/comments":        `[]`,
		"/api/issues/10/history":         `[]`,
		"/api/issues/10/attachments":     `[]`,
		"/api/issues?limit=100&offset=0": `{"issues":[{"id":13,"project_id":4,"issue_key":"OLD-13","type":"ticket","title":"Deleted project issue","status":"open"}],"has_more":false}`,
		"/api/issues/trash":              `[]`,
	}
	source, closeServer := fakeSourceRoutes(t, routes, nil)
	defer closeServer()
	report, err := (Importer{Source: source}).Run(context.Background(), "test", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["projects"] != 1 || report.Counts["ticket"] != 1 || report.Counts["skipped"] != 3 || report.Counts["skipped_projects"] != 1 || report.Counts["skipped_issues"] != 2 {
		t.Fatalf("wrong counts: %+v", report.Counts)
	}
	want := []SkippedItem{
		{Type: "issue", ID: 11, Path: "/issues/11/relations", Status: 404},
		{Type: "issue", ID: 12, Path: "/issues/12", Status: 404},
		{Type: "project", ID: 4, Path: "/projects/4/issues", Status: 404},
	}
	if !reflect.DeepEqual(report.Skipped, want) {
		t.Fatalf("skipped = %+v, want %+v", report.Skipped, want)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"skipped_projects":1`) || !strings.Contains(string(encoded), `"path":"/issues/11/relations"`) {
		t.Fatalf("skips absent from JSON report: %s", encoded)
	}
}

func TestSourceKnowledge404SkipsProject(t *testing.T) {
	routes := map[string]string{
		"/api/projects?status=all":     `[{"id":3,"key":"PAI","name":"Paimos","status":"active"}]`,
		"/api/projects?status=deleted": `[]`,
		"/api/users":                   `[]`,
		"/api/users?status=deleted":    `[]`,
		"/api/projects/3/issues":       `[]`,
	}
	source, closeServer := fakeSourceRoutes(t, routes, nil)
	defer closeServer()
	report, err := (Importer{Source: source}).Run(context.Background(), "test", "PAI", true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["projects"] != 0 || !reflect.DeepEqual(report.Skipped, []SkippedItem{{Type: "project", ID: 3, Path: "/projects/3/knowledge", Status: 404}}) {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestSourceNon404AbortsWithSafeMethodAndPath(t *testing.T) {
	routes := map[string]string{
		"/api/projects?status=all":     `[{"id":3,"key":"PAI","name":"Paimos","status":"active"}]`,
		"/api/projects?status=deleted": `[]`,
		"/api/users":                   `[]`,
		"/api/users?status=deleted":    `[]`,
	}
	source, closeServer := fakeSourceRoutes(t, routes, map[string]int{"/api/projects/3/issues": 500})
	defer closeServer()
	_, err := (Importer{Source: source}).Run(context.Background(), "test", "", true)
	if err == nil || err.Error() != "source GET /projects/3/issues returned HTTP 500" {
		t.Fatalf("unexpected source error: %v", err)
	}
	if strings.Contains(err.Error(), "fake-key") || strings.Contains(err.Error(), "/api/") {
		t.Fatalf("source error leaked request detail: %v", err)
	}
}

func fakeSourceRoutes(t *testing.T, routes map[string]string, statuses map[string]int) (*HTTPSource, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fake-key" {
			t.Error("unexpected source request method or authorization")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		path := r.URL.RequestURI()
		if status := statuses[path]; status != 0 {
			w.WriteHeader(status)
			return
		}
		body, ok := routes[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	file := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(file, []byte("fake-key"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewHTTPSource(server.URL, file, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return source, server.Close
}
