// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMigration194PreservesOnlySealProvenBaselineRevision(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m194.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, statement := range []string{
		`CREATE TABLE schema_versions(version INTEGER PRIMARY KEY)`,
		`CREATE TABLE baseline_batch_drafts(
		 id INTEGER PRIMARY KEY,project_id INTEGER NOT NULL,revision INTEGER NOT NULL,status TEXT NOT NULL,
		 baseline_ref TEXT NOT NULL,baseline_revision INTEGER NOT NULL,content_digest TEXT NOT NULL,
		 revision_seal TEXT NOT NULL,stream_ref TEXT NOT NULL,bounded_content_json TEXT NOT NULL)`,
		`CREATE TABLE baseline_batch_batches(
		 id INTEGER PRIMARY KEY,project_id INTEGER NOT NULL,draft_id INTEGER NOT NULL,draft_revision INTEGER NOT NULL,
		 baseline_ref TEXT NOT NULL,content_digest TEXT NOT NULL,revision_seal TEXT NOT NULL,stream_ref TEXT NOT NULL)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	const (
		baselineRef = "baseline:4"
		digest      = "sha256:61533137085ab8b98f6d8af687ab5633139210317fb958e5309ea9eeffc28c4c"
		seal        = "sha256:2de396581ea365e9b10a90f8761ad4550babb6ec92b08c0600fedb08379903ae"
		streamRef   = "stream:inspr397"
	)
	if got := legacyBaselineRevisionSeal(baselineRef, 1, digest); got != seal {
		t.Fatalf("migration seal oracle=%s want ticket-observed Aithema seal %s", got, seal)
	}
	snapshot := func(revision int64, revisionSeal string) string {
		raw, err := json.Marshal(legacyBaselineSnapshot{
			HandoverVersion: "aithema.handover/0.1", StreamRef: streamRef,
			ExportedAt: "2026-09-11T10:00:00.000Z", BaselineRef: baselineRef,
			Revision: revision, ContentDigest: digest, RevisionSeal: revisionSeal,
			Requirements: json.RawMessage(`[{"requirement_ref":"req.fullstream","statement":"Run the contained fullstream","acceptance_criteria":["Preserve immutable baseline identity"],"constraint_refs":[]}]`),
			Constraints:  json.RawMessage(`[]`), PendingProposals: json.RawMessage(`[]`),
			ImportedClaimedApprovedBy: "party:aithema-reviewer", Authenticity: "untrusted_imported_claim",
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	// draft revision 7 deliberately differs from baseline revision 1.
	if _, err := database.Exec(`INSERT INTO baseline_batch_drafts VALUES
		(10,1,7,'closed',?,?,?,?,?,?),
		(11,1,8,'closed',?,?,?,?,?,?)`,
		baselineRef, 1, digest, seal, streamRef, snapshot(1, seal),
		baselineRef, 2, digest, seal, streamRef, snapshot(2, seal)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO baseline_batch_batches VALUES
		(20,1,10,7,?,?,?,?),
		(21,1,11,8,?,?,?,?)`,
		baselineRef, digest, seal, streamRef,
		baselineRef, digest, seal, streamRef); err != nil {
		t.Fatal(err)
	}

	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := applyBaselineRevisionMigration194(context.Background(), conn); err != nil {
		t.Fatal(err)
	}

	var proven, unknown sql.NullInt64
	if err := database.QueryRow(`SELECT baseline_revision FROM baseline_batch_batches WHERE id=20`).Scan(&proven); err != nil {
		t.Fatal(err)
	}
	if !proven.Valid || proven.Int64 != 1 {
		t.Fatalf("seal-proven revision=%v want 1", proven)
	}
	if err := database.QueryRow(`SELECT baseline_revision FROM baseline_batch_batches WHERE id=21`).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if unknown.Valid {
		t.Fatalf("unproven historical revision was invented: %v", unknown)
	}
	if _, err := database.Exec(`UPDATE baseline_batch_batches SET baseline_revision=2 WHERE id=20`); err == nil {
		t.Fatal("immutable preserved baseline revision was mutable")
	}
	var applied int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=194`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("M194 application count=%d err=%v", applied, err)
	}
}
