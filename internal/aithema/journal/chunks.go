// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/jackc/pgx/v5"
)

const journalChunkBytes = 256 << 10
const maxUnfinishedUploads = 64

// Staging is durable across replicas but never advances a journal cursor.
// The caller holds the session lock and rechecks delegated authority per chunk.
func (s *Store) upload(ctx context.Context, tx pgx.Tx, st *session, c tokens.Claims, action string, raw []byte, q url.Values) (json.RawMessage, error) {
	sha := q.Get("upload")
	if !in(action, "records", "snapshots") || !sha256Pattern.MatchString(sha) || q.Get("ack") != "seq" {
		return nil, fault(400, "invalid_request")
	}
	doc, err := decode(raw)
	if err != nil {
		return nil, err
	}
	offset, offsetOK := integer(doc["offset"])
	total, totalOK := integer(doc["total"])
	encoded, chunkOK := doc["chunk"].(string)
	if len(doc) != 3 || !offsetOK || !totalOK || total <= 1<<20 || offset >= total || !chunkOK || len(encoded) > base64.StdEncoding.EncodedLen(journalChunkBytes) {
		return nil, fault(400, "invalid_request")
	}
	part, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(part) != encoded || int64(len(part)) != min(int64(journalChunkBytes), total-offset) {
		return nil, fault(400, "invalid_request")
	}
	key := []any{st.Tenant, st.ID, c.Generation, c.AuthEpoch, action, sha}
	const where = `tenant_id=$1 AND sid=$2 AND worker_generation=$3 AND auth_epoch=$4 AND action=$5 AND wire_sha256=$6`
	var stagedTotal, next int64
	err = tx.QueryRow(ctx, `SELECT total,next_offset FROM aithema_journal_uploads WHERE `+where, key...).Scan(&stagedTotal, &next)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if offset == 0 {
		if errors.Is(err, pgx.ErrNoRows) {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM aithema_journal_uploads WHERE tenant_id=$1 AND sid=$2`, st.Tenant, st.ID).Scan(&count); err != nil {
				return nil, err
			}
			if count >= maxUnfinishedUploads {
				return nil, fault(413, "too_large")
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM aithema_journal_uploads WHERE `+where, key...); err != nil {
			return nil, err
		}
		args := append(key, total)
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_journal_uploads(tenant_id,sid,worker_generation,auth_epoch,action,wire_sha256,total,next_offset) VALUES($1,$2,$3,$4,$5,$6,$7,0)`, args...); err != nil {
			return nil, err
		}
	} else if errors.Is(err, pgx.ErrNoRows) || total != stagedTotal || offset != next {
		return nil, fault(400, "invalid_request")
	}
	args := append(append([]any{}, key...), offset, part)
	if _, err := tx.Exec(ctx, `INSERT INTO aithema_journal_upload_chunks(tenant_id,sid,worker_generation,auth_epoch,action,wire_sha256,chunk_offset,bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, args...); err != nil {
		return nil, err
	}
	next = offset + int64(len(part))
	if next < total {
		if _, err := tx.Exec(ctx, `UPDATE aithema_journal_uploads SET next_offset=$7 WHERE `+where, append(key, next)...); err != nil {
			return nil, err
		}
		return marshal(map[string]any{"offset": next, "total": total}), nil
	}
	rows, err := tx.Query(ctx, `SELECT bytes FROM aithema_journal_upload_chunks WHERE `+where+` ORDER BY chunk_offset`, key...)
	if err != nil {
		return nil, err
	}
	var complete bytes.Buffer
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			rows.Close()
			return nil, err
		}
		complete.Write(chunk)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if int64(complete.Len()) != total || digest(complete.Bytes()) != sha {
		return nil, fault(409, "idempotency_conflict")
	}
	record, err := s.append(ctx, tx, st, c, action, complete.Bytes())
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM aithema_journal_uploads WHERE `+where, key...); err != nil {
		return nil, err
	}
	return sequenceAck(record)
}

func readChunk(ctx context.Context, tx pgx.Tx, st *session, q url.Values) (json.RawMessage, error) {
	if !queryValid(q, "format", "ids", "offset", "length") || !q.Has("offset") {
		return nil, fault(400, "invalid_request")
	}
	seq, seqErr := count(q.Get("ids"), 0)
	offset, offsetErr := count(q.Get("offset"), -1)
	length, lengthErr := count(q.Get("length"), journalChunkBytes)
	if seqErr != nil || seq < 1 || offsetErr != nil || offset < 0 || lengthErr != nil || length < 1 || length > journalChunkBytes {
		return nil, fault(400, "invalid_request")
	}
	var total int64
	var part []byte
	// PostgreSQL bytea range reads keep each transfer bounded, including rows
	// whose original UTF-8 document spans many transport chunks.
	err := tx.QueryRow(ctx, `SELECT octet_length(original_bytes),substring(original_bytes FROM $4::integer FOR $5::integer) FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND seq=$3`, st.Tenant, st.ID, seq, min(offset+1, int64(1<<31-1)), length).Scan(&total, &part)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fault(404, "not_found")
	}
	if err != nil {
		return nil, err
	}
	if offset >= total {
		return nil, fault(400, "invalid_request")
	}
	return marshal(map[string]any{"seq": seq, "offset": offset, "total": total, "chunk": base64.StdEncoding.EncodeToString(part)}), nil
}

func clearUploads(ctx context.Context, tx pgx.Tx, st *session) error {
	_, err := tx.Exec(ctx, `DELETE FROM aithema_journal_uploads WHERE tenant_id=$1 AND sid=$2`, st.Tenant, st.ID)
	return err
}
