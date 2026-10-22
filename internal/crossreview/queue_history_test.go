// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

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
	"time"
)

func TestMergeQueueHistoryRequiresCanonicalCompleteTimeline(t *testing.T) {
	// Risk: any old enqueue, a transient group SHA, partial pagination or an
	// administrator's merge is incorrectly treated as canonical queue proof.
	sha := strings.Repeat("c", 40)
	at := time.Date(2026, 10, 22, 17, 19, 0, 0, time.UTC)
	for _, scenario := range []string{"merged", "manual", "wrong-sha", "wrong-pr", "error", "missing-add", "out-of-order", "partial", "repeated-cursor", "page-cap"} {
		t.Run(scenario, func(t *testing.T) {
			reads := 0
			queued, err := readMergeQueueProof(874, sha, func(cursor *string, out any) error {
				reads++
				pr, canonical, reason := int64(874), sha, "merged"
				if scenario == "error" {
					return errors.New("fixture transport failure")
				}
				if scenario == "partial" && reads == 2 {
					return errors.New("fixture truncated history")
				}
				if scenario == "wrong-sha" {
					canonical = strings.Repeat("d", 40)
				}
				if scenario == "wrong-pr" {
					pr++
				}
				if scenario == "manual" {
					reason = "manual"
				}
				nodes := []map[string]any{
					{"__typename": "AddedToMergeQueueEvent", "id": "added", "createdAt": at.Add(-20 * time.Minute)},
					{"__typename": "RemovedFromMergeQueueEvent", "id": "removed", "createdAt": at, "reason": reason},
				}
				if scenario == "missing-add" {
					nodes = nodes[1:]
				}
				if scenario == "out-of-order" {
					nodes[0], nodes[1] = nodes[1], nodes[0]
				}
				more, end := scenario == "partial" || scenario == "repeated-cursor" || scenario == "page-cap", "cursor"
				if scenario == "page-cap" || scenario == "repeated-cursor" {
					nodes = []map[string]any{{"__typename": "AddedToMergeQueueEvent", "id": "add-" + strings.Repeat("a", reads), "createdAt": at.Add(-20 * time.Minute)},
						{"__typename": "RemovedFromMergeQueueEvent", "id": "remove-" + strings.Repeat("a", reads), "createdAt": at.Add(-20 * time.Minute), "reason": "manual"}}
					if scenario == "page-cap" {
						end += strings.Repeat("a", reads)
					}
				}
				raw, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
					"number": pr, "merged": true, "mergedAt": at, "mergeCommit": map[string]string{"oid": canonical},
					"timelineItems": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": more, "endCursor": end}},
				}}}})
				return json.Unmarshal(raw, out)
			})
			if scenario == "merged" || scenario == "manual" {
				if err != nil || queued != (scenario == "merged") {
					t.Fatalf("queued=%v err=%v", queued, err)
				}
			} else if queued || err == nil {
				t.Fatalf("unproven queue provenance accepted: queued=%v err=%v reads=%d", queued, err, reads)
			}
			if scenario == "page-cap" && reads != 10 {
				t.Fatalf("did not exercise pagination cap: %d", reads)
			}
		})
	}
	// Exercise the real capability's token request and fixed GraphQL transport.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "fixture-signing")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	revoked, queries := false, 0
	app := &GitHubApp{Config: AppConfig{ID: "123", InstallationID: "456", TenantID: testID(), Repository: "example/delivery", KeyFile: keyFile}}
	app.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body any
		switch r.Method + " " + r.URL.Path {
		case "POST /app/installations/456/access_tokens":
			var in struct {
				Permissions map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if len(in.Permissions) != 2 || in.Permissions["pull_requests"] != "read" || in.Permissions["metadata"] != "read" {
				t.Fatalf("queue history widened token permissions: %v", in.Permissions)
			}
			body = map[string]any{"token": "fixture-read-token", "permissions": in.Permissions, "repositories": []map[string]string{{"full_name": app.Config.Repository}}}
		case "POST /graphql":
			queries++
			var in struct{ Query string }
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !strings.HasPrefix(in.Query, "query(") || strings.Contains(in.Query, "mutation") || !strings.Contains(in.Query, "timelineItems(first:100") {
				t.Fatal("non-fixed query", err)
			}
			body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
				"number": 874, "merged": true, "mergedAt": at, "mergeCommit": map[string]string{"oid": sha},
				"timelineItems": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}},
			}}}}
		case "DELETE /installation/token":
			revoked = true
			body = map[string]any{}
		default:
			t.Fatalf("unexpected queue-history request: %s %s", r.Method, r.URL.Path)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
	})}
	queued, err := app.MergeQueueProof(t.Context(), 874, sha)
	if err != nil || queued || !revoked || queries != 1 {
		t.Fatalf("queue history transport: queued=%v revoked=%v queries=%d err=%v", queued, revoked, queries, err)
	}
}
