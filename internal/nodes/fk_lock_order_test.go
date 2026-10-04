// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// A comment owns node SHARE before appending its tenant-referencing event.
// Pause the edit after its tenant fence, then prove it waits on that same node.
// The comment must still commit: tenant UPDATE would create a real FK cycle.
// Both helper entry orders are exercised; deadlines only guard against hangs.
func TestNodeWritesAllowConcurrentCommentTenantFK(t *testing.T) {
	for _, name := range []string{"tree-first-update", "moderation-first-delete"} {
		t.Run(name, func(t *testing.T) {
			p := newPrincipal(t, "comment-fk")
			kind := kindBySlug(t, p, "ticket").ID
			node := mustCreateNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Original ticket"}`, kind))
			commentPool, commentBarrier, commentCtx := dbtest.BarrierPool(t, appPool, func(sql string) bool {
				return strings.Contains(sql, "FROM nodes") && strings.Contains(sql, "FOR SHARE")
			})
			editPool, editBarrier, editCtx := dbtest.BarrierPool(t, appPool, func(sql string) bool {
				return strings.Contains(sql, "FROM tenants") && strings.Contains(sql, " FOR ")
			})
			commentCtx, cancelComment := context.WithCancel(commentCtx)
			editCtx, cancelEdit := context.WithCancel(editCtx)
			defer cancelComment()
			defer cancelEdit()
			commentMux, editMux := http.NewServeMux(), http.NewServeMux()
			activity.New(commentPool).Mount(commentMux)
			New(editPool, nil).Mount(editMux)
			commentRequest := httptest.NewRequest(http.MethodPost, "/api/nodes/"+node.ID+"/comments",
				strings.NewReader(`{"body_markdown":"Concurrent comment"}`)).WithContext(tenant.WithPrincipal(commentCtx, p))
			method, body, event, wantStatus := http.MethodPatch, `{"title":"Edited ticket"}`, "node.updated", http.StatusOK
			if name == "moderation-first-delete" {
				method, body, event, wantStatus = http.MethodDelete, "", "node.deleted", http.StatusNoContent
			}
			editRequest := httptest.NewRequest(method, "/api/nodes/"+node.ID, strings.NewReader(body)).WithContext(tenant.WithPrincipal(editCtx, p))
			commentRequest.Header.Set("Content-Type", "application/json")
			editRequest.Header.Set("Content-Type", "application/json")
			commentResponse, editResponse := httptest.NewRecorder(), httptest.NewRecorder()
			commentDone, editDone := make(chan struct{}), make(chan struct{})
			go func() { defer close(commentDone); commentMux.ServeHTTP(commentResponse, commentRequest) }()
			t.Cleanup(func() {
				cancelComment()
				cancelEdit()
				commentBarrier.Release()
				editBarrier.Release()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				dbtest.Await(t, cleanup, commentDone)
			})
			commentPID := commentBarrier.Wait(t, commentCtx)
			go func() { defer close(editDone); editMux.ServeHTTP(editResponse, editRequest) }()
			t.Cleanup(func() {
				cancelEdit()
				editBarrier.Release()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				dbtest.Await(t, cleanup, editDone)
			})
			editPID := editBarrier.Wait(t, editCtx)
			if editPID == commentPID {
				t.Fatal("edit and comment must use separate transactions")
			}
			editBarrier.Release()
			if lock := dbtest.BlockedOrDone(t, editCtx, adminPool, commentPID, editDone); lock != "transactionid" && lock != "tuple" {
				t.Fatalf("edit must overlap the comment and wait for its node lock, got %q", lock)
			}
			commentBarrier.Release()
			dbtest.Await(t, commentCtx, commentDone)
			dbtest.Await(t, editCtx, editDone)
			if commentResponse.Code != http.StatusCreated {
				t.Errorf("comment must commit while edit waits: %d %s", commentResponse.Code, commentResponse.Body.String())
			}
			if editResponse.Code != wantStatus {
				t.Errorf("edit must commit after comment: %d %s, want %d", editResponse.Code, editResponse.Body.String(), wantStatus)
			}
			// Retain the node (deletion is soft) and prove both effects and their
			// exact audit events survived. A successful response alone is insufficient.
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				var title string
				var deleted bool
				if err := tx.QueryRow(t.Context(), `SELECT title,deleted_at IS NOT NULL FROM nodes WHERE id=$1`, node.ID).Scan(&title, &deleted); err != nil {
					return err
				}
				wantTitle := "Edited ticket"
				if method == http.MethodDelete {
					wantTitle = "Original ticket"
				}
				if title != wantTitle || deleted != (method == http.MethodDelete) {
					return fmt.Errorf("durable node title=%q deleted=%t", title, deleted)
				}
				var comments, edits int
				if err := tx.QueryRow(t.Context(), `SELECT
					count(*) FILTER (WHERE type='comment.created' AND after->>'body_markdown'='Concurrent comment'),
					count(*) FILTER (WHERE type=$2)
					FROM events WHERE node_id=$1 AND actor_principal_id=$3`, node.ID, event, p.ID).Scan(&comments, &edits); err != nil {
					return err
				}
				if comments != 1 || edits != 1 {
					return fmt.Errorf("durable audit comments=%d edits=%d, want one each", comments, edits)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
