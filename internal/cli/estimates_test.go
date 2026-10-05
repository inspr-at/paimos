// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIssueEstimateRejectsConcurrentPersonEstimate(t *testing.T) {
	for _, args := range [][]string{
		{"issue", "update", "AEON-1", "--priority", "low"},
		{"issue", "update", "AEON-1", "--estimate", "3h"},
		{"issue", "estimate", "AEON-1", "--hours", "3h", "--source", "agent"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			isolate(t)
			revision := time.Date(2026, 9, 29, 10, 0, 0, 123456000, time.UTC)
			ticket := apiNode{ID: transcriptEntryID, Key: "AEON-1", KindID: "ticket-kind", UpdatedAt: revision,
				Fields: json.RawMessage(`{"estimate_hours":2,"estimate_source":"agent","estimate_by":"draft-worker"}`)}
			personFields := json.RawMessage(`{"estimate_hours":5,"estimate_source":"person","estimate_by":"person","estimate_confirmed":true}`)
			writes, rejected := 0, false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
					json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "ticket-kind", Slug: "ticket"}}})
				case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
					json.NewEncoder(w).Encode(nodePage{Items: []apiNode{ticket}})
					// A person changes the estimate after the CLI's read snapshot.
					ticket.Fields = personFields
					ticket.UpdatedAt = revision.Add(time.Microsecond)
				case r.Method == http.MethodPatch && r.URL.Path == "/api/nodes/"+ticket.ID:
					writes++
					if got := r.Header.Get("If-Unmodified-Since"); got != revision.Format(time.RFC3339Nano) {
						t.Errorf("revision = %q, want original read timestamp", got)
					}
					if header := r.Header.Get("If-Unmodified-Since"); header != "" && header != ticket.UpdatedAt.Format(time.RFC3339Nano) {
						rejected = true
						w.WriteHeader(http.StatusPreconditionFailed)
						w.Write([]byte(`{"error":"node has changed"}`))
						return
					}
					var patch struct {
						Fields json.RawMessage `json:"fields"`
					}
					json.NewDecoder(r.Body).Decode(&patch)
					ticket.Fields = patch.Fields
					json.NewEncoder(w).Encode(ticket)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", testKey)
			code, out, stderr := runCLI(append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}, args...), "")
			if code != 1 || !strings.Contains(stderr, "api 412: node has changed") || !strings.Contains(stderr, "nothing was written") || out != "" {
				t.Fatalf("stale write: exit %d, stdout %q, stderr %q", code, out, stderr)
			}
			if writes != 1 || !rejected || string(ticket.Fields) != string(personFields) {
				t.Fatalf("person estimate was not preserved: writes=%d rejected=%v fields=%s", writes, rejected, ticket.Fields)
			}
		})
	}
}

func TestParseEstimate(t *testing.T) {
	for value, want := range map[string]float64{"2h": 2, "90m": 1.5, "1.5": 1.5, "30m": .5, ".5h": .5, " 200 ": 200, "12000m": 200} {
		got, err := parseEstimate(value)
		if err != nil || got != want {
			t.Fatalf("%q = %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "0", "-1h", "200.1", "12001m", "NaN", "Inf", "1e2", "2 h", "2hours", "1h30m", "1.", "+2"} {
		if got, err := parseEstimate(value); err == nil {
			t.Errorf("%q accepted: %v", value, got)
		}
	}
}

func TestEstimateMissingMigratedWorkLeaves(t *testing.T) {
	for _, scenario := range []string{"dry-run", "apply", "stale-revision", "parent", "unknown-shape", "missing-revision"} {
		t.Run(scenario, func(t *testing.T) {
			isolate(t)
			leaf, parent := true, false
			revision := time.Date(2026, 10, 5, 8, 0, 0, 123456000, time.UTC)
			missing := apiNode{ID: transcriptEntryID, Key: "AEON-1", KindID: "work-kind", IsLeaf: &leaf, UpdatedAt: revision,
				Fields: json.RawMessage(`{"priority":"high","estimate_source":"agent","estimate_by":"old-worker","estimate_confirmed":true}`)}
			existing := apiNode{ID: transcriptSessionID, Key: "AEON-2", KindID: "work-kind", IsLeaf: &leaf, UpdatedAt: revision,
				Fields: json.RawMessage(`{"estimate_hours":5,"estimate_source":"person","estimate_confirmed":true}`)}
			group := apiNode{ID: "parent-node", Key: "AEON-3", KindID: "work-kind", IsLeaf: &parent, UpdatedAt: revision, Fields: json.RawMessage(`{}`)}
			if scenario == "unknown-shape" {
				missing.IsLeaf = nil
			}
			if scenario == "missing-revision" {
				missing.UpdatedAt = time.Time{}
			}
			original, person := string(missing.Fields), `{"estimate_hours":7,"estimate_source":"person","estimate_confirmed":true}`
			writes, pages := 0, 0
			var mu sync.Mutex
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
					// Retired kinds may still be registered, but all migrated nodes use work.
					json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "project-kind", Slug: "project"}, {ID: "work-kind", Slug: "work"}, {ID: "ticket-kind", Slug: "ticket"}, {ID: "task-kind", Slug: "task"}}})
				case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
					if r.URL.Query().Get("kind_id") == "project-kind" {
						json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptProjectID, Key: "PRJ-1", KindID: "project-kind", Fields: json.RawMessage(`{"project_key":"AEON"}`)}}})
						return
					}
					if q := r.URL.Query(); q.Get("within") != transcriptProjectID || q.Get("kind") != "work" || q.Get("shape") != "leaf" || q.Get("limit") != "200" {
						t.Errorf("missing canonical bounded leaf scope: %s", r.URL)
					}
					pages++
					if r.URL.Query().Get("cursor") == "" {
						cursor := "next-page"
						json.NewEncoder(w).Encode(nodePage{Items: []apiNode{missing}, NextCursor: &cursor})
						if scenario == "stale-revision" {
							// The encoded read snapshot is fixed before the concurrent person write.
							missing.Fields = json.RawMessage(person)
							missing.UpdatedAt = revision.Add(time.Microsecond)
						}
					} else {
						// Even an inconsistent server response must not make a parent writable.
						json.NewEncoder(w).Encode(nodePage{Items: []apiNode{existing, group}})
					}
				case r.Method == http.MethodPatch && r.URL.Path == "/api/nodes/"+missing.ID:
					writes++
					if got := r.Header.Get("If-Unmodified-Since"); got != revision.Format(time.RFC3339Nano) {
						t.Errorf("revision = %q, want the read snapshot", got)
					}
					if r.Header.Get("If-Unmodified-Since") != missing.UpdatedAt.Format(time.RFC3339Nano) {
						w.WriteHeader(http.StatusPreconditionFailed)
						w.Write([]byte(`{"error":"node has changed"}`))
						return
					}
					var patch struct {
						Fields map[string]any `json:"fields"`
					}
					if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if f := patch.Fields; f["estimate_hours"] != float64(2) || f["estimate_source"] != "agent" || f["priority"] != "high" || f["estimate_by"] != nil || f["estimate_at"] != nil || f["estimate_confirmed"] != nil {
						t.Errorf("invalid estimate or forged provenance: %#v", f)
					}
					missing.Fields, _ = json.Marshal(patch.Fields)
					json.NewEncoder(w).Encode(missing)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", testKey)
			mode := "--apply"
			if scenario == "dry-run" {
				mode = "--dry-run"
			}
			plan := `[{"key":"AEON-1","hours":2},{"key":"AEON-2","hours":3}]`
			if scenario == "parent" {
				plan = `[{"key":"AEON-1","hours":2},{"key":"AEON-3","hours":3}]`
			}
			code, out, stderr := runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "issue", "estimate", "--missing", "--project", "AEON", "--from-file", "-", mode}, plan)
			mu.Lock()
			defer mu.Unlock()
			if pages != 2 {
				t.Fatalf("read %d pages, want the complete project snapshot", pages)
			}
			switch scenario {
			case "dry-run", "apply":
				status, wantWrites := "planned", 0
				if scenario == "apply" {
					status, wantWrites = "applied", 1
				}
				if code != 0 || stderr != "" || writes != wantWrites || !strings.Contains(out, "AEON-1: 2h ("+status+")") || !strings.Contains(out, "AEON-2: 3h (skipped: already estimated)") {
					t.Fatalf("%s: exit %d writes %d stdout %q stderr %q", scenario, code, writes, out, stderr)
				}
				if scenario == "dry-run" && string(missing.Fields) != original {
					t.Fatal("dry-run mutated the leaf")
				}
			case "stale-revision":
				if code != 1 || writes != 1 || out != "" || !strings.Contains(stderr, "api 412: node has changed") || !strings.Contains(stderr, "stopped at AEON-1") || string(missing.Fields) != person {
					t.Fatalf("stale estimate accepted: exit %d writes %d stdout %q stderr %q fields %s", code, writes, out, stderr, missing.Fields)
				}
			default:
				want := "AEON-1 is not a visible work leaf"
				if scenario == "parent" {
					want = "AEON-3 is not a visible work leaf"
				} else if scenario == "missing-revision" {
					want = "AEON-1 has no revision timestamp"
				}
				if code == 0 || writes != 0 || out != "" || !strings.Contains(stderr, want) || string(missing.Fields) != original {
					t.Fatalf("invalid target wrote: exit %d writes %d stdout %q stderr %q", code, writes, out, stderr)
				}
			}
			if string(existing.Fields) != `{"estimate_hours":5,"estimate_source":"person","estimate_confirmed":true}` || string(group.Fields) != `{}` {
				t.Fatal("existing estimate or parent changed")
			}
		})
	}
}

func TestEstimateCLIFlagsAndPlan(t *testing.T) {
	isolate(t)
	revision := "2026-09-29T10:00:00.123456Z"
	ticket := apiNode{ID: transcriptEntryID, Key: "AEON-1", KindID: "ticket-kind", Fields: json.RawMessage(`{"priority":"high"}`)}
	_ = json.Unmarshal([]byte(`"`+revision+`"`), &ticket.UpdatedAt)
	existing := apiNode{ID: transcriptSessionID, Key: "AEON-2", KindID: "ticket-kind", Fields: json.RawMessage(`{"estimate_hours":5}`), UpdatedAt: ticket.UpdatedAt}
	var writes []map[string]any
	conflict := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(map[string]any{"items": []apiKind{{ID: "project-kind", Slug: "project"}, {ID: "ticket-kind", Slug: "ticket"}, {ID: "task-kind", Slug: "task"}}})
		case r.Method == "GET" && r.URL.Path == "/api/nodes":
			if r.URL.Query().Get("kind_id") == "project-kind" {
				json.NewEncoder(w).Encode(map[string]any{"items": []apiNode{{ID: transcriptProjectID, Key: "PRJ-1", KindID: "project-kind", Fields: json.RawMessage(`{"project_key":"AEON"}`)}}})
				return
			}
			if r.URL.Query().Get("within") != "" && r.URL.Query().Get("within") != transcriptProjectID {
				t.Error("wrong project scope")
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []apiNode{ticket, existing}})
		case r.Method == "PATCH" || r.Method == "POST":
			if r.Header.Get("X-Aeon-Client") != "" {
				t.Error("CLI must not ask the API to rephrase warnings")
			}
			if r.Method == "PATCH" && r.Header.Get("If-Unmodified-Since") != revision {
				t.Error("missing revision")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			writes = append(writes, body)
			if conflict {
				w.WriteHeader(412)
				w.Write([]byte(`{"error":"node has changed"}`))
				return
			}
			json.NewEncoder(w).Encode(ticket)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	run := func(args []string, stdin string) (int, string, string) {
		return runCLI(append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}, args...), stdin)
	}
	for _, args := range [][]string{{"issue", "create", "--project", "AEON", "--title", "Work", "--estimate", "90m"}, {"issue", "create", "--project", "AEON", "--title", "Work", "--priority", "high", "--estimate-hours", "1.5"}, {"issue", "update", "AEON-1", "--estimate", "1.5"}, {"issue", "update", "AEON-1", "--estimate-hours", "1.5"}, {"issue", "estimate", "AEON-1", "--hours", "1.5", "--source", "agent"}} {
		code, out, err := run(args, "")
		if code != 0 {
			t.Fatalf("%v: %d %s %s", args, code, out, err)
		}
		fields := writes[len(writes)-1]["fields"].(map[string]any)
		if fields["estimate_hours"] != 1.5 {
			t.Fatal(fields)
		}
		if fields["estimate_by"] != nil || fields["estimate_at"] != nil {
			t.Fatal("client forged provenance")
		}
	}
	code, out, stderr := run([]string{"issue", "create", "--project", "AEON", "--title", "Unestimated", "--type", "task"}, "")
	if code != 0 || !strings.Contains(stderr, "--estimate-hours") {
		t.Fatalf("missing create hint: %d %s %s", code, out, stderr)
	}
	code, _, stderr = run([]string{"issue", "create", "--project", "AEON", "--title", "Estimated", "--estimate-hours", "1.5"}, "")
	if code != 0 || strings.Contains(stderr, "--estimate-hours") {
		t.Fatalf("estimated create warned: %d %s", code, stderr)
	}
	base := []string{"issue", "estimate", "--missing", "--project", "AEON", "--from-file", "-"}
	plan := `[{"key":"AEON-1","hours":2},{"key":"AEON-2","hours":3}]`
	before := len(writes)
	code, out, err := run(append(base, "--dry-run"), plan)
	if code != 0 || len(writes) != before || !strings.Contains(out, "skipped: already estimated") {
		t.Fatalf("dry %d %s %s", code, out, err)
	}
	for _, invalid := range []string{`[{"key":"AEON-1","hours":2},{"key":"FOREIGN-1","hours":3}]`, `[{"key":"AEON-1","hours":2},{"key":"AEON-1","hours":3}]`, `[{"key":"AEON-1","hours":201}]`, plan + ` []`, `[{"key":"AEON-1","hours":2,"source":"person"}]`} {
		code, _, _ = run(append(base, "--apply"), invalid)
		if code == 0 || len(writes) != before {
			t.Fatal("invalid plan wrote", invalid)
		}
	}
	code, out, err = run(append(base, "--apply"), plan)
	if code != 0 || len(writes) != before+1 {
		t.Fatalf("apply %d %s %s", code, out, err)
	}
	fields := writes[len(writes)-1]["fields"].(map[string]any)
	if fields["estimate_source"] != "agent" || fields["priority"] != "high" {
		t.Fatal(fields)
	}
	conflict = true
	code, _, err = run(append(base, "--apply"), plan)
	if code == 0 || !strings.Contains(err, "stopped at AEON-1") {
		t.Fatal("conflict accepted", err)
	}
}

func TestIssueUpdateRefusesMissingRevision(t *testing.T) {
	isolate(t)
	ticket := apiNode{ID: transcriptEntryID, Key: "AEON-1", KindID: "ticket-kind", Fields: json.RawMessage(`{"priority":"low"}`)}
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "ticket-kind", Slug: "ticket"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{ticket}})
		case r.Method == http.MethodPatch:
			writes++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	for _, args := range [][]string{
		{"issue", "update", "AEON-1", "--priority", "high"},
		{"issue", "estimate", "AEON-1", "--hours", "2h"},
	} {
		writes = 0
		code, out, stderr := runCLI(append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}, args...), "")
		if code == 0 || writes != 0 || out != "" || !strings.Contains(stderr, "no revision timestamp") || !strings.Contains(stderr, "nothing was written") {
			t.Fatalf("%v: exit %d writes %d stdout %q stderr %q", args, code, writes, out, stderr)
		}
	}
}

func TestIssueCreateAddsEstimateHint(t *testing.T) {
	isolate(t)
	const warning = "add an agent-hours estimate in fields.estimate_hours (for example 2 or 0.5)"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Aeon-Client") != "" {
			t.Error("CLI must not ask the API to rephrase warnings")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/kinds":
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "project-kind", Slug: "project"}, {ID: "ticket-kind", Slug: "ticket"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/nodes":
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: transcriptProjectID, Key: "PRJ-1", KindID: "project-kind", Fields: json.RawMessage(`{"project_key":"AEON"}`)}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/nodes":
			json.NewEncoder(w).Encode(apiNode{ID: transcriptEntryID, Key: "AEON-9", KindID: "ticket-kind", Title: "Draft", State: "open", Warnings: []string{warning}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	run := func(args []string) (int, string, string) {
		return runCLI(append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}, args...), "")
	}
	code, out, stderr := run([]string{"issue", "create", "--project", "AEON", "--title", "Draft"})
	if code != 0 || !strings.Contains(out, "AEON-9") || !strings.Contains(stderr, warning) || !strings.Contains(stderr, "--estimate") {
		t.Fatalf("text create: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run([]string{"--json", "issue", "create", "--project", "AEON", "--title", "Draft"})
	if code != 0 || !strings.Contains(stderr, "--estimate") {
		t.Fatalf("json create stderr: %d %q", code, stderr)
	}
	var view issueView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(view.Warnings, "\n")
	if !strings.Contains(joined, "fields.estimate_hours") || !strings.Contains(joined, "--estimate") {
		t.Fatalf("json warnings: %v", view.Warnings)
	}
}
