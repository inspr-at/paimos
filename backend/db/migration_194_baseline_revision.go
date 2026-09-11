// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

const baselineRevisionSealPrefix = "sha256:"

type legacyBaselineSnapshot struct {
	HandoverVersion           string          `json:"handover_version"`
	StreamRef                 string          `json:"stream_ref"`
	ExportedAt                string          `json:"exported_at"`
	BaselineRef               string          `json:"baseline_ref"`
	Revision                  int64           `json:"revision"`
	ContentDigest             string          `json:"content_digest"`
	RevisionSeal              string          `json:"revision_seal"`
	Requirements              json.RawMessage `json:"requirements"`
	Constraints               json.RawMessage `json:"constraints"`
	PendingProposals          json.RawMessage `json:"pending_proposals"`
	ImportedClaimedApprovedBy string          `json:"imported_claimed_approved_by"`
	Authenticity              string          `json:"authenticity"`
}

type legacyBaselineCandidate struct {
	batchID          int64
	projectID        int64
	draftID          int64
	draftRevision    int64
	baselineRef      string
	contentDigest    string
	revisionSeal     string
	streamRef        string
	draftProjectID   int64
	draftRowRevision int64
	draftStatus      string
	draftBaselineRef string
	draftBaselineRev int64
	draftDigest      string
	draftSeal        string
	draftStreamRef   string
	boundedContent   string
}

// applyBaselineRevisionMigration194 adds the missing immutable snapshot field.
// Historical recovery is deliberately narrow: only the linked closed draft's
// bounded Aithema snapshot may supply a candidate, and its revision must
// recompute the immutable seal already copied onto the batch. Every other old
// row remains NULL rather than borrowing draft_revision or mutable project data.
func applyBaselineRevisionMigration194(ctx context.Context, conn *sql.Conn) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration 194: begin tx: %w", err)
	}
	defer tx.Rollback()

	alter := `ALTER TABLE baseline_batch_batches ADD COLUMN baseline_revision INTEGER
		CHECK(baseline_revision IS NULL OR baseline_revision>0)`
	if _, err := tx.ExecContext(ctx, alter); err != nil {
		return migrationStepError(194, alter, err)
	}

	rows, err := tx.QueryContext(ctx, `SELECT
		b.id,b.project_id,b.draft_id,b.draft_revision,b.baseline_ref,b.content_digest,b.revision_seal,b.stream_ref,
		d.project_id,d.revision,d.status,d.baseline_ref,d.baseline_revision,d.content_digest,d.revision_seal,d.stream_ref,
		d.bounded_content_json
		FROM baseline_batch_batches b
		JOIN baseline_batch_drafts d ON d.id=b.draft_id
		WHERE b.baseline_revision IS NULL`)
	if err != nil {
		return fmt.Errorf("migration 194: select legacy baseline snapshots: %w", err)
	}
	var candidates []legacyBaselineCandidate
	for rows.Next() {
		var candidate legacyBaselineCandidate
		if err := rows.Scan(
			&candidate.batchID, &candidate.projectID, &candidate.draftID, &candidate.draftRevision,
			&candidate.baselineRef, &candidate.contentDigest, &candidate.revisionSeal, &candidate.streamRef,
			&candidate.draftProjectID, &candidate.draftRowRevision, &candidate.draftStatus,
			&candidate.draftBaselineRef, &candidate.draftBaselineRev, &candidate.draftDigest,
			&candidate.draftSeal, &candidate.draftStreamRef, &candidate.boundedContent,
		); err != nil {
			rows.Close()
			return fmt.Errorf("migration 194: scan legacy baseline snapshot: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("migration 194: iterate legacy baseline snapshots: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("migration 194: close legacy baseline snapshots: %w", err)
	}

	for _, candidate := range candidates {
		if !candidate.exactRevisionProven() {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_batches SET baseline_revision=?
			WHERE id=? AND baseline_revision IS NULL`, candidate.draftBaselineRev, candidate.batchID); err != nil {
			return fmt.Errorf("migration 194: preserve proven baseline revision: %w", err)
		}
	}

	trigger := `CREATE TRIGGER trg_baseline_batch_baseline_revision_immutable
		BEFORE UPDATE OF baseline_revision ON baseline_batch_batches
		BEGIN SELECT RAISE(ABORT,'baseline batch revision is immutable'); END`
	if _, err := tx.ExecContext(ctx, trigger); err != nil {
		return migrationStepError(194, trigger, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_versions(version) VALUES(194)"); err != nil {
		return fmt.Errorf("record migration 194: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration 194: commit: %w", err)
	}
	return nil
}

func (candidate legacyBaselineCandidate) exactRevisionProven() bool {
	if candidate.draftStatus != "closed" || candidate.draftBaselineRev < 1 ||
		candidate.projectID != candidate.draftProjectID ||
		candidate.draftRevision != candidate.draftRowRevision ||
		candidate.baselineRef != candidate.draftBaselineRef ||
		candidate.contentDigest != candidate.draftDigest ||
		candidate.revisionSeal != candidate.draftSeal ||
		candidate.streamRef != candidate.draftStreamRef {
		return false
	}
	var snapshot legacyBaselineSnapshot
	decoder := json.NewDecoder(bytes.NewBufferString(candidate.boundedContent))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false
	}
	if snapshot.HandoverVersion != "aithema.handover/0.1" ||
		snapshot.Authenticity != "untrusted_imported_claim" ||
		snapshot.Revision != candidate.draftBaselineRev ||
		snapshot.BaselineRef != candidate.baselineRef ||
		snapshot.ContentDigest != candidate.contentDigest ||
		snapshot.RevisionSeal != candidate.revisionSeal ||
		snapshot.StreamRef != candidate.streamRef {
		return false
	}
	return legacyBaselineRevisionSeal(snapshot.BaselineRef, snapshot.Revision, snapshot.ContentDigest) == candidate.revisionSeal
}

func legacyBaselineRevisionSeal(baselineRef string, revision int64, contentDigest string) string {
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		BaselineRef   string `json:"baseline_ref"`
		Revision      int64  `json:"revision"`
		ContentDigest string `json:"content_digest"`
	}{baselineRef, revision, contentDigest}); err != nil {
		return ""
	}
	raw := legacyAithemaJSONStringifyEscapes(bytes.TrimSuffix(payload.Bytes(), []byte{'\n'}))
	digest := sha256.Sum256(raw)
	return baselineRevisionSealPrefix + hex.EncodeToString(digest[:])
}

// legacyAithemaJSONStringifyEscapes mirrors the three encoding/json differences
// from JavaScript JSON.stringify used by the imported Aithema seal contract.
func legacyAithemaJSONStringifyEscapes(in []byte) []byte {
	if !bytes.Contains(in, []byte(`\u`)) {
		return in
	}
	out := make([]byte, 0, len(in))
	for index := 0; index < len(in); {
		if in[index] != '\\' || index+1 >= len(in) {
			out = append(out, in[index])
			index++
			continue
		}
		if in[index+1] == 'u' && index+5 < len(in) {
			switch string(in[index+2 : index+6]) {
			case "2028":
				out = append(out, "\u2028"...)
				index += 6
				continue
			case "2029":
				out = append(out, "\u2029"...)
				index += 6
				continue
			case "0008":
				out = append(out, '\\', 'b')
				index += 6
				continue
			case "000c":
				out = append(out, '\\', 'f')
				index += 6
				continue
			}
		}
		out = append(out, in[index], in[index+1])
		index += 2
	}
	return out
}

func checkM194SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	var collisions int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM pragma_table_info('baseline_batch_batches') WHERE name='baseline_revision') +
		(SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='trg_baseline_batch_baseline_revision_immutable')`).Scan(&collisions); err != nil {
		return fmt.Errorf("inspect M194 baseline revision ownership: %w", err)
	}
	if collisions != 0 {
		return fmt.Errorf("M194 schema is partially present or locally incompatible: baseline revision objects=%d", collisions)
	}
	return nil
}
