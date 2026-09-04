// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

func TestKnowledgeIdentityConcurrentCreateReturnsOneCreatedAndConflicts(t *testing.T) {
	// These cases deliberately use the same slug in three disjoint scopes. One
	// real server preserves that cross-scope coexistence proof without rebuilding
	// the complete production migration chain before every sequential subtest.
	ts := newTestServer(t)
	tests := []struct {
		name     string
		endpoint func(*testing.T, *testServer) string
	}{
		{
			name: "project",
			endpoint: func(t *testing.T, ts *testServer) string {
				projectID := createTestProject(t, ts, "Concurrent project identity", "CPI")
				return knowledgeURL(projectID, "memory")
			},
		},
		{name: "user", endpoint: func(_ *testing.T, _ *testServer) string { return userMemoryURL }},
		{name: "instance", endpoint: func(_ *testing.T, _ *testServer) string { return instanceMemoryURL }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint := test.endpoint(t, ts)
			payload, err := json.Marshal(map[string]any{
				"slug": "one_identity", "title": "One identity",
			})
			if err != nil {
				t.Fatal(err)
			}

			const attempts = 8
			type result struct {
				status int
				err    error
			}
			results := make(chan result, attempts)
			start := make(chan struct{})
			var workers sync.WaitGroup
			for range attempts {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.srv.URL+endpoint, bytes.NewReader(payload))
					if err != nil {
						results <- result{err: err}
						return
					}
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Cookie", ts.adminCookie)
					response, err := http.DefaultClient.Do(req)
					if err != nil {
						results <- result{err: err}
						return
					}
					response.Body.Close()
					results <- result{status: response.StatusCode}
				}()
			}
			close(start)
			workers.Wait()
			close(results)

			created, conflicts := 0, 0
			for result := range results {
				if result.err != nil {
					t.Fatalf("concurrent create failed: %v", result.err)
				}
				switch result.status {
				case http.StatusCreated:
					created++
				case http.StatusConflict:
					conflicts++
				default:
					t.Fatalf("concurrent create status=%d, want 201 or 409", result.status)
				}
			}
			if created != 1 || conflicts != attempts-1 {
				t.Fatalf("created=%d conflicts=%d, want 1/%d", created, conflicts, attempts-1)
			}
		})
	}
}

func TestKnowledgeIdentityRenameConflictsReturn409(t *testing.T) {
	tests := []struct {
		name      string
		endpoints func(*testing.T, *testServer) (collection, first string)
	}{
		{
			name: "project",
			endpoints: func(t *testing.T, ts *testServer) (string, string) {
				projectID := createTestProject(t, ts, "Rename project identity", "RPI")
				return knowledgeURL(projectID, "memory"), knowledgeEntryURL(projectID, "memory", "first")
			},
		},
		{
			name: "user",
			endpoints: func(_ *testing.T, _ *testServer) (string, string) {
				return userMemoryURL, userMemoryEntryURL("first")
			},
		},
		{
			name: "instance",
			endpoints: func(_ *testing.T, _ *testServer) (string, string) {
				return instanceMemoryURL, instanceMemoryEntryURL("first")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts := newTestServer(t)
			collection, first := test.endpoints(t, ts)
			for _, slug := range []string{"first", "second"} {
				response := ts.post(t, collection, ts.adminCookie, map[string]any{"slug": slug, "title": slug})
				assertStatus(t, response, http.StatusCreated)
			}
			response := ts.put(t, first, ts.adminCookie, map[string]any{"slug": "second", "title": "collision"})
			assertStatus(t, response, http.StatusConflict)
		})
	}
}

func TestKnowledgeIdentityCanBeRecreatedAfterSoftDelete(t *testing.T) {
	tests := []struct {
		name      string
		endpoints func(*testing.T, *testServer) (collection, entry string)
	}{
		{
			name: "project",
			endpoints: func(t *testing.T, ts *testServer) (string, string) {
				projectID := createTestProject(t, ts, "Recreate project identity", "RDI")
				return knowledgeURL(projectID, "memory"), knowledgeEntryURL(projectID, "memory", "recreated")
			},
		},
		{name: "user", endpoints: func(_ *testing.T, _ *testServer) (string, string) {
			return userMemoryURL, userMemoryEntryURL("recreated")
		}},
		{name: "instance", endpoints: func(_ *testing.T, _ *testServer) (string, string) {
			return instanceMemoryURL, instanceMemoryEntryURL("recreated")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts := newTestServer(t)
			collection, entry := test.endpoints(t, ts)
			payload := map[string]any{"slug": "recreated", "title": "First"}
			assertStatus(t, ts.post(t, collection, ts.adminCookie, payload), http.StatusCreated)
			assertStatus(t, ts.del(t, entry, ts.adminCookie), http.StatusNoContent)
			payload["title"] = "Second"
			assertStatus(t, ts.post(t, collection, ts.adminCookie, payload), http.StatusCreated)
		})
	}
}

func TestKnowledgeIdentityRestoreAfterReuseReturns409(t *testing.T) {
	tests := []struct {
		name      string
		endpoints func(*testing.T, *testServer) (collection, entry string)
	}{
		{
			name: "project",
			endpoints: func(t *testing.T, ts *testServer) (string, string) {
				projectID := createTestProject(t, ts, "Restore project identity", "RST")
				return knowledgeURL(projectID, "memory"), knowledgeEntryURL(projectID, "memory", "reused")
			},
		},
		{name: "user", endpoints: func(_ *testing.T, _ *testServer) (string, string) {
			return userMemoryURL, userMemoryEntryURL("reused")
		}},
		{name: "instance", endpoints: func(_ *testing.T, _ *testServer) (string, string) {
			return instanceMemoryURL, instanceMemoryEntryURL("reused")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts := newTestServer(t)
			collection, entry := test.endpoints(t, ts)
			first := ts.post(t, collection, ts.adminCookie, map[string]any{"slug": "reused", "title": "First"})
			assertStatus(t, first, http.StatusCreated)
			oldID := responseID(t, first)
			assertStatus(t, ts.del(t, entry, ts.adminCookie), http.StatusNoContent)
			assertStatus(t, ts.post(t, collection, ts.adminCookie, map[string]any{"slug": "reused", "title": "Second"}), http.StatusCreated)

			restore := ts.post(t, "/api/issues/"+itoa(oldID)+"/restore", ts.adminCookie, nil)
			assertStatus(t, restore, http.StatusConflict)
			var body struct {
				Error string `json:"error"`
			}
			decode(t, restore, &body)
			if !strings.Contains(body.Error, `memory "reused"`) || !strings.Contains(body.Error, test.name+"-scope identity") {
				t.Fatalf("restore conflict=%q, want named %s identity", body.Error, test.name)
			}
		})
	}
}

func TestKnowledgeIdentityUndoDeleteAfterReuseReturns409(t *testing.T) {
	tests := []struct {
		name      string
		endpoints func(*testing.T, *testServer) (collection, entry string)
	}{
		{
			name: "project",
			endpoints: func(t *testing.T, ts *testServer) (string, string) {
				projectID := createTestProject(t, ts, "Undo project identity", "UND")
				return knowledgeURL(projectID, "memory"), knowledgeEntryURL(projectID, "memory", "reused")
			},
		},
		{name: "user", endpoints: func(_ *testing.T, _ *testServer) (string, string) {
			return userMemoryURL, userMemoryEntryURL("reused")
		}},
		{name: "instance", endpoints: func(_ *testing.T, _ *testServer) (string, string) {
			return instanceMemoryURL, instanceMemoryEntryURL("reused")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts := newTestServer(t)
			collection, entry := test.endpoints(t, ts)
			first := ts.post(t, collection, ts.adminCookie, map[string]any{"slug": "reused", "title": "First"})
			assertStatus(t, first, http.StatusCreated)
			oldID := responseID(t, first)
			assertStatus(t, ts.del(t, entry, ts.adminCookie), http.StatusNoContent)
			assertStatus(t, ts.post(t, collection, ts.adminCookie, map[string]any{"slug": "reused", "title": "Second"}), http.StatusCreated)

			var logID int64
			if err := db.DB.QueryRow(`SELECT id FROM mutation_log
				WHERE subject_type='issue' AND subject_id=? AND mutation_type='issue.delete'
				ORDER BY id DESC LIMIT 1`, oldID).Scan(&logID); err != nil {
				t.Fatalf("find delete mutation: %v", err)
			}
			undo := ts.post(t, "/api/undo/"+itoa(logID), ts.adminCookie, map[string]any{})
			assertStatus(t, undo, http.StatusConflict)
			var body struct {
				Error string `json:"error"`
			}
			decode(t, undo, &body)
			if !strings.Contains(body.Error, `memory "reused"`) || !strings.Contains(body.Error, test.name+"-scope identity") {
				t.Fatalf("undo conflict=%q, want named %s identity", body.Error, test.name)
			}
			var oldStillTrashed, undoStillActive int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM issues WHERE id=? AND deleted_at IS NOT NULL`, oldID).Scan(&oldStillTrashed); err != nil {
				t.Fatal(err)
			}
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mutation_log
				WHERE id=? AND undone_at IS NULL AND on_user_stack=1`, logID).Scan(&undoStillActive); err != nil {
				t.Fatal(err)
			}
			if oldStillTrashed != 1 || undoStillActive != 1 {
				t.Fatalf("conflicting undo mutated state: oldStillTrashed=%d undoStillActive=%d", oldStillTrashed, undoStillActive)
			}
		})
	}
}
