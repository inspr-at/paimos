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
const maxStagedBytes = 16 << 20
const maxInlineBytes = 1 << 20
const maxResponseBytes = 4 << 20

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
	// Reject before decoding a chunk, creating staging, or allocating the
	// completion buffer. The 16 MiB ceiling includes the 13 MB contract case.
	if total > maxStagedBytes {
		return nil, fault(413, "too_large")
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
	complete.Grow(int(total))
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			rows.Close()
			return nil, err
		}
		if int64(complete.Len()+len(chunk)) > total {
			rows.Close()
			return nil, fault(409, "idempotency_conflict")
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
	// octet_length reads TOAST size metadata without detoasting the document.
	err := tx.QueryRow(ctx, `SELECT octet_length(original_bytes) FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND seq=$3`, st.Tenant, st.ID, seq).Scan(&total)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fault(404, "not_found")
	}
	if err != nil {
		return nil, err
	}
	if offset >= total {
		return nil, fault(400, "invalid_request")
	}
	length = min(length, total-offset)
	if total <= maxInlineBytes {
		// This fallback may detoast at most 1 MiB, including pre-AIT-89 rows.
		if err := tx.QueryRow(ctx, `SELECT substring(original_bytes FROM $4::integer FOR $5::integer) FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND seq=$3 AND octet_length(original_bytes)<=$6`, st.Tenant, st.ID, seq, offset+1, length, maxInlineBytes).Scan(&part); err != nil {
			return nil, err
		}
	} else {
		start := offset / journalChunkBytes * journalChunkBytes
		end := (offset + length - 1) / journalChunkBytes * journalChunkBytes
		// An arbitrary range crosses at most two independently toasted chunks.
		rows, err := tx.Query(ctx, `SELECT chunk_offset,bytes FROM aithema_journal_record_chunks WHERE tenant_id=$1 AND sid=$2 AND seq=$3 AND chunk_offset BETWEEN $4 AND $5 ORDER BY chunk_offset`, st.Tenant, st.ID, seq, start, end)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		part = make([]byte, 0, int(length))
		next := start
		for rows.Next() {
			var position int64
			var chunk []byte
			if err := rows.Scan(&position, &chunk); err != nil {
				return nil, err
			}
			if position != next || int64(len(chunk)) != min(int64(journalChunkBytes), total-position) {
				return nil, fault(503, "unavailable")
			}
			lo, hi := max(offset-position, 0), min(offset+length-position, int64(len(chunk)))
			part = append(part, chunk[lo:hi]...)
			next += journalChunkBytes
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if int64(len(part)) != length {
			return nil, fault(503, "unavailable")
		}
	}
	return marshal(map[string]any{"seq": seq, "offset": offset, "total": total, "chunk": base64.StdEncoding.EncodeToString(part)}), nil
}

func clearUploads(ctx context.Context, tx pgx.Tx, st *session) error {
	_, err := tx.Exec(ctx, `DELETE FROM aithema_journal_uploads WHERE tenant_id=$1 AND sid=$2`, st.Tenant, st.ID)
	return err
}
