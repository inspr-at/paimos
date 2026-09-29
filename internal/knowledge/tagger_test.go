// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestTaggerWithoutOutcomeTable(t *testing.T) {
	f := setup(t)
	recent := addNode(t, f, "DONE-1", "ticket", "Rotate the tokens", &f.project)
	closeAged(t, f, recent, "done", "1 hour")
	stale := addNode(t, f, "DONE-2", "ticket", "Too old to nominate", &f.project)
	closeAged(t, f, stale, "done", "48 hours")
	open := addNode(t, f, "OPEN-1", "ticket", "Still open", &f.project)
	task := addNode(t, f, "TASK-9", "task", "Closed task", &f.project)
	closeAged(t, f, task, "done", "1 hour")

	verdict := addComment(t, f, recent, "VERDICT pass. The rotation held.")
	lower := addComment(t, f, recent, "the verdict was informal")
	incident := addComment(t, f, recent, "The incident stopped the deploy.")
	side := addComment(t, f, recent, "An incidental remark about naming.")
	tagged := addComment(t, f, recent, "#process-learning The incident was already tagged.")

	n, err := TagOnce(t.Context(), f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("nominated %d", n)
	}
	if origin, excerpt, ok := nominationOfKey(t, f, nodeLearningID(recent)); !ok || origin != "closed_ticket" || excerpt != "Rotate the tokens" || !nodeTagged(t, f, recent) {
		t.Fatalf("recent %q %q %v tagged %v", origin, excerpt, ok, nodeTagged(t, f, recent))
	}
	for _, id := range []string{stale, open, task} {
		if nodeTagged(t, f, id) {
			t.Fatalf("tagged %s", id)
		}
		if _, _, ok := nominationOfKey(t, f, nodeLearningID(id)); ok {
			t.Fatalf("nominated %s", id)
		}
	}
	if origin, _, ok := nominationOfKey(t, f, commentLearningID(recent, verdict)); !ok || origin != "review_verdict" {
		t.Fatalf("verdict comment %q %v", origin, ok)
	}
	for _, id := range []string{lower, side, tagged} {
		if _, _, ok := nominationOfKey(t, f, commentLearningID(recent, id)); ok {
			t.Fatalf("nominated comment %s", id)
		}
	}
	if decisionCount(t, f) != 0 {
		t.Fatal("tagger wrote a decision")
	}
	if !verdictsNull(t, f) {
		t.Fatal("missing outcome table advanced verdicts_until")
	}
	again, err := TagOnce(t.Context(), f.db.App)
	if err != nil || again != 0 {
		t.Fatalf("second pass %d %v", again, err)
	}
	page := listLearningsHTTP(t, f, f.a, f.project)
	incidentID := commentLearningID(recent, incident)
	if !hasLearning(page.Items, incidentID) {
		t.Fatal("incident comment missing from the inbox")
	}
	w := call(t, f, f.a, "POST", "/api/knowledge/learnings/"+incidentID+"/dismiss", map[string]any{})
	expect(t, w, 200)
	if decisionOf(t, f, incidentID) != "dismissed" {
		t.Fatalf("dismiss %q", decisionOf(t, f, incidentID))
	}
}

func TestTaggerSingleRunner(t *testing.T) {
	f := setup(t)
	ticket := addNode(t, f, "DONE-3", "ticket", "Wait for the lock", &f.project)
	closeAged(t, f, ticket, "done", "1 hour")
	conn, err := f.db.App.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, taggerLockKey)
		conn.Release()
	})
	var locked bool
	if err = conn.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, taggerLockKey).Scan(&locked); err != nil || !locked {
		t.Fatal(err, locked)
	}
	n, err := TagOnce(t.Context(), f.db.App)
	if err != nil || n != 0 || nodeTagged(t, f, ticket) {
		t.Fatalf("held lock tagged %d %v %v", n, err, nodeTagged(t, f, ticket))
	}
	if _, err = conn.Exec(t.Context(), `SELECT pg_advisory_unlock($1)`, taggerLockKey); err != nil {
		t.Fatal(err)
	}
	n, err = TagOnce(t.Context(), f.db.App)
	if err != nil || n != 1 || !nodeTagged(t, f, ticket) {
		t.Fatalf("after unlock %d %v tagged %v", n, err, nodeTagged(t, f, ticket))
	}
}

func TestTaggerBatchCursor(t *testing.T) {
	f := setup(t)
	previous := taggerBatch
	taggerBatch = 1
	t.Cleanup(func() { taggerBatch = previous })
	older := addNode(t, f, "DONE-4", "ticket", "Older closed ticket", &f.project)
	newer := addNode(t, f, "DONE-5", "ticket", "Newer closed ticket", &f.project)
	closeAged(t, f, older, "done", "2 hours")
	closeAged(t, f, newer, "done", "1 hour")
	n, err := TagOnce(t.Context(), f.db.App)
	if err != nil || n != 1 || !nodeTagged(t, f, older) || nodeTagged(t, f, newer) {
		t.Fatalf("first %d %v older %v newer %v", n, err, nodeTagged(t, f, older), nodeTagged(t, f, newer))
	}
	n, err = TagOnce(t.Context(), f.db.App)
	if err != nil || n != 1 || !nodeTagged(t, f, newer) {
		t.Fatalf("second %d %v newer %v", n, err, nodeTagged(t, f, newer))
	}
}

func TestTaggerOutcomeVerdicts(t *testing.T) {
	f := setup(t)
	installOutcomeTable(t, f)
	ticket := addNode(t, f, "REV-1", "ticket", "Review the rotation", &f.project)
	err := db.InTenant(db.AllProjects(t.Context(), "outcome fixture"), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO outcome_events (tenant_id, kind, project_id, ticket_node_id, payload)
			VALUES ($1, 'review_verdict', $2::uuid, $3::uuid, '{"verdict":"pass","summary":"The rotation held"}'::jsonb)`,
			f.a.TenantID, f.project, ticket)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	verdict := addComment(t, f, ticket, "VERDICT pass in the thread.")
	incident := addComment(t, f, ticket, "The incident page is the source.")
	n, err := TagOnce(t.Context(), f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("nominated %d", n)
	}
	origin, excerpt, ok := nominationOfKey(t, f, nodeLearningID(ticket))
	if !ok || origin != "review_verdict" || excerpt != "The rotation held" || !nodeTagged(t, f, ticket) {
		t.Fatalf("verdict %q %q %v", origin, excerpt, ok)
	}
	if _, _, ok := nominationOfKey(t, f, commentLearningID(ticket, verdict)); ok {
		t.Fatal("outcome table still nominated a VERDICT comment")
	}
	if origin, _, ok := nominationOfKey(t, f, commentLearningID(ticket, incident)); !ok || origin != "incident_comment" {
		t.Fatalf("incident %q %v", origin, ok)
	}
	if verdictsNull(t, f) {
		t.Fatal("usable outcome table left verdicts_until null")
	}
	if decisionCount(t, f) != 0 {
		t.Fatal("outcome tagger wrote a decision")
	}
}

func closeAged(t *testing.T, f fixture, id, state, age string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `UPDATE nodes SET state=$3, updated_at=clock_timestamp() - $4::interval WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, id, state, age)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("node not closed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func nominationOfKey(t *testing.T, f fixture, key string) (string, string, bool) {
	t.Helper()
	var origin, excerpt string
	err := f.db.Admin.QueryRow(t.Context(), `SELECT origin, excerpt FROM method_learning_nominations WHERE tenant_id=$1 AND source_key=$2`, f.a.TenantID, key).Scan(&origin, &excerpt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return origin, excerpt, true
}

func nodeTagged(t *testing.T, f fixture, id string) bool {
	t.Helper()
	var raw []byte
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT coalesce(fields, '{}'::jsonb) FROM nodes WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return hasLearningTag(raw)
}

func decisionCount(t *testing.T, f fixture) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM method_learning_decisions WHERE tenant_id=$1`, f.a.TenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func verdictsNull(t *testing.T, f fixture) bool {
	t.Helper()
	var null bool
	err := f.db.Admin.QueryRow(t.Context(), `SELECT verdicts_until IS NULL FROM method_learning_tag_cursor WHERE tenant_id=$1`, f.a.TenantID).Scan(&null)
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	return null
}

func installOutcomeTable(t *testing.T, f fixture) {
	t.Helper()
	statements := []string{
		`CREATE TABLE outcome_events (
			tenant_id uuid NOT NULL,
			id uuid NOT NULL DEFAULT gen_random_uuid(),
			kind text NOT NULL,
			project_id uuid NOT NULL,
			ticket_node_id uuid NOT NULL,
			payload jsonb NOT NULL,
			recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
			PRIMARY KEY (tenant_id, id))`,
		`ALTER TABLE outcome_events ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE outcome_events FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY outcome_events_tenant ON outcome_events
			USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
			WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)`,
		`CREATE POLICY outcome_events_project_visibility ON outcome_events AS RESTRICTIVE
			USING ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
			WITH CHECK ((SELECT aeon_visible_all()) OR project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))`,
	}
	for _, statement := range statements {
		if _, err := f.db.App.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
}
