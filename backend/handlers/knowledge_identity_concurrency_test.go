// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

func TestKnowledgeIdentityConcurrentCreateReturnsOneCreatedAndConflicts(t *testing.T) {
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
			ts := newTestServer(t)
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
