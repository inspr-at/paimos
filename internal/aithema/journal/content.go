// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/jackc/pgx/v5"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (v *Validator) contentValid(doc map[string]any) bool {
	data := object(doc["data"])
	raw := []byte(text(data["canonical"]))
	if number(doc["minor"]) < 2 || number(doc["min_reader"]) < 2 || int64(len(raw)) != number(data["size"]) || digest(raw) != text(data["sha256"]) {
		return false
	}
	content, err := decodeJSON(raw)
	if err != nil || !v.check(v.resolve("pending-op-content.schema.json", "/$defs/content"), content, "pending-op-content.schema.json", 0) {
		return false
	}
	canonical, err := tokens.CanonicalJSON(raw)
	return err == nil && bytes.Equal(canonical, raw)
}

func (v *Validator) pendingReference(payload string) (map[string]any, error) {
	limit := number(v.resolve("spec-snapshot.schema.json", "/$defs/pending_op/properties/payload")["maxLength"])
	if int64(len(payload)) > limit {
		return nil, fault(422, "citation_invalid")
	}
	ref, err := decode([]byte(payload))
	if err != nil || !v.check(v.resolve("pending-op-content.schema.json", "/$defs/reference"), ref, "pending-op-content.schema.json", 0) {
		return nil, fault(422, "citation_invalid")
	}
	canonical, err := tokens.CanonicalJSON([]byte(payload))
	if err != nil || string(canonical) != payload {
		return nil, fault(422, "citation_invalid")
	}
	return ref, nil
}

// Content must already have been acknowledged in this session. A snapshot is
// never permission to regenerate missing content or to execute reference JSON.
func (s *Store) verifyReferences(ctx context.Context, tx pgx.Tx, st *session, doc map[string]any) error {
	for _, value := range array(doc["pending_ops"]) {
		op := object(value)
		if op["payload_kind"] != "pending_op.content" {
			continue
		}
		ref, err := s.validator.pendingReference(text(op["payload"]))
		if err != nil {
			return err
		}
		seq := number(ref["record_seq"])
		if seq > st.Seq {
			return fault(422, "citation_invalid")
		}
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT document FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND seq=$3 AND kind='pending_op.content'`, st.Tenant, st.ID, seq).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return fault(422, "citation_invalid")
		}
		if err != nil {
			return err
		}
		content, err := decodeDocument(raw)
		data := object(content["data"])
		if err != nil || content["sid"] != st.ID || number(content["seq"]) != seq || data["sha256"] != ref["sha256"] || number(data["size"]) != number(ref["size"]) || !s.validator.contentValid(content) {
			return fault(422, "citation_invalid")
		}
	}
	return nil
}

func deduplicateContent(ctx context.Context, tx pgx.Tx, st *session, raw []byte, doc map[string]any) (Record, bool, error) {
	var original, projection []byte
	err := tx.QueryRow(ctx, `SELECT original_bytes,document FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND content_sha256=$3`, st.Tenant, st.ID, text(object(doc["data"])["sha256"])).Scan(&original, &projection)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	prior, err := decodeDocument(projection)
	if err != nil {
		return Record{}, false, err
	}
	if !equal(prior["data"], doc["data"]) {
		return Record{}, false, fault(409, "idempotency_conflict")
	}
	_, err = tx.Exec(ctx, `INSERT INTO aithema_journal_content_events(tenant_id,sid,client_event_id,seq,original_bytes) VALUES($1,$2,$3,$4,$5)`, st.Tenant, st.ID, text(doc["client_event_id"]), number(prior["seq"]), raw)
	return Record{Document: projection, Bytes: string(original)}, true, err
}

func sequenceAck(record Record) (json.RawMessage, error) {
	// Projection was produced or read by the store; do not apply a request-size
	// limit while making a bounded acknowledgement for a large document.
	doc, err := decodeJSON(record.Document)
	if err != nil {
		return nil, err
	}
	seq, ok := integer(doc["seq"])
	if !ok || seq < 1 {
		return nil, fault(503, "unavailable")
	}
	return marshal(map[string]any{"seq": seq}), nil
}

func contentSequences(ctx context.Context, tx pgx.Tx, st *session, q url.Values) (json.RawMessage, error) {
	if !queryValid(q, "format", "digests") {
		return nil, fault(400, "invalid_request")
	}
	digests := strings.Split(q.Get("digests"), ",")
	if len(digests) > 1000 {
		return nil, fault(400, "invalid_request")
	}
	for _, sha := range digests {
		if !sha256Pattern.MatchString(sha) {
			return nil, fault(400, "invalid_request")
		}
	}
	rows, err := tx.Query(ctx, `SELECT content_sha256,seq FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND content_sha256=ANY($3::text[])`, st.Tenant, st.ID, digests)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]int64{}
	for rows.Next() {
		var sha string
		var seq int64
		if err := rows.Scan(&sha, &seq); err != nil {
			return nil, err
		}
		found[sha] = seq
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs := []map[string]any{}
	seen := map[string]bool{}
	for _, sha := range digests {
		if seq, ok := found[sha]; ok && !seen[sha] {
			refs = append(refs, map[string]any{"seq": seq})
		}
		seen[sha] = true
	}
	return marshal(refs), nil
}
