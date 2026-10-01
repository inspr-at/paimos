// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/jackc/pgx/v5"
)

// Record carries the host projection and the exact submitted bytes separately.
// Projection is the mock-kit wire format; stored envelopes support lossless
// JournalPort hydration without reconstructing bytes from JSONB or a map.
type Record struct {
	Document json.RawMessage `json:"document"`
	Bytes    string          `json:"bytes"`
}

func appendRecord(ctx context.Context, tx pgx.Tx, st *session, raw []byte, doc map[string]any) (Record, error) {
	if st.Seq == tokens.MaxSafeInteger {
		return Record{}, fault(409, "sequence_exhausted")
	}
	st.Seq++
	// Decode validated UTF-8 once, retaining json.Number until all integer and
	// digest checks are done. Original bytes are never reserialized for storage.
	doc["seq"] = st.Seq
	returned := marshal(doc)
	kind := text(doc["kind"])
	if doc["contract"] == "aithema.spec.snapshot" {
		kind = "spec.snapshot"
	}
	_, err := tx.Exec(ctx, `INSERT INTO aithema_journal_records(tenant_id,sid,seq,client_event_id,contract,kind,original_bytes,document) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, st.Tenant, st.ID, st.Seq, text(doc["client_event_id"]), text(doc["contract"]), kind, raw, returned)
	if err != nil {
		return Record{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE aithema_sessions SET seq=$3 WHERE tenant_id=$1 AND sid=$2`, st.Tenant, st.ID, st.Seq); err != nil {
		return Record{}, err
	}
	return Record{Document: returned, Bytes: string(raw)}, nil
}
func formatRecord(record Record, stored bool) any {
	if stored {
		return record
	}
	return record.Document
}
func storedFormat(q url.Values) (bool, error) {
	if len(q["format"]) > 1 {
		return false, fault(400, "invalid_request")
	}
	switch q.Get("format") {
	case "", "projection":
		return false, nil
	case "stored":
		return true, nil
	default:
		return false, fault(400, "invalid_request")
	}
}
func count(s string, fallback int64) (int64, error) {
	if s == "" {
		return fallback, nil
	}
	if s != "0" && (s[0] < '1' || s[0] > '9') {
		return 0, fault(400, "invalid_request")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fault(400, "invalid_request")
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > tokens.MaxSafeInteger {
		return 0, fault(400, "invalid_request")
	}
	return n, nil
}
func queryValid(q url.Values, fields ...string) bool {
	for key, values := range q {
		if len(values) != 1 || !in(key, fields...) {
			return false
		}
	}
	return true
}

type Result struct {
	Status int
	Body   json.RawMessage
}

// Request executes an already cryptographically verified delegated request.
// All scope and freshness checks happen again under the projection row lock.
func (s *Store) Request(ctx context.Context, c tokens.Claims, area, action, method string, raw []byte, q url.Values) (Result, error) {
	return s.request(ctx, c, area, action, method, raw, q, nil)
}

func (s *Store) request(ctx context.Context, c tokens.Claims, area, action, method string, raw []byte, q url.Values, verifyAtUse func() error) (Result, error) {
	out := Result{Status: 200}
	cap := "aithema.ledger"
	if area == "journal" {
		cap = "aithema.journal.read"
		if method == "POST" {
			cap = "aithema.journal.write"
		}
		if action == "authority" {
			cap = "aithema.authority.read"
		}
	}
	err := s.transaction(ctx, c.TenantID, c.SessionID, area == "ledger" && method == "POST", func(tx pgx.Tx, st *session) error {
		if verifyAtUse != nil {
			if err := verifyAtUse(); err != nil {
				return err
			}
		}
		if err := authorize(st, c, cap); err != nil {
			return err
		}
		settling := area == "ledger" && action == "settle" && method == "POST"
		// Authority must report tombstones to the polling service; settlement
		// remains available to owners of already committed claims. Neither
		// exception permits new effects after offboarding or plugin removal.
		if s.HostAuthorization != nil && cap != "aithema.authority.read" && !settling {
			if err := s.HostAuthorization(ctx, tx, c, cap, state(st, s.now())); err != nil {
				return err
			}
		}
		if cap != "aithema.authority.read" && !settling {
			if err := fence(st, c, method == "POST"); err != nil {
				return err
			}
		}
		if st.Suspended && method == "POST" && (area == "journal" || action == "admit" || action == "claim") {
			return fault(409, "suspended")
		}
		if method == "GET" || method == "HEAD" {
			if area == "journal" && action == "authority" && s.AuthorityProjection != nil {
				if !queryValid(q) {
					return fault(400, "invalid_request")
				}
				projection, err := s.AuthorityProjection(ctx, tx, state(st, s.now()))
				if err != nil {
					return err
				}
				out.Body = marshal(projection)
				return nil
			}
			body, err := s.read(ctx, tx, st, area, action, q)
			out.Body = body
			return err
		}
		if area == "journal" {
			record, err := s.append(ctx, tx, st, c, action, raw)
			if err != nil {
				return err
			}
			stored, err := storedFormat(q)
			if err != nil || !queryValid(q, "format") {
				return fault(400, "invalid_request")
			}
			out.Body = marshal(formatRecord(record, stored))
			return nil
		}
		if !queryValid(q) {
			return fault(400, "invalid_request")
		}
		var err error
		out, err = s.ledger(ctx, tx, st, c, action, raw)
		return err
	})
	return out, err
}
func (s *Store) append(ctx context.Context, tx pgx.Tx, st *session, c tokens.Claims, action string, raw []byte) (Record, error) {
	contract := "aithema.journal.record"
	if action == "snapshots" {
		contract = "aithema.spec.snapshot"
	}
	doc, err := decode(raw)
	if err != nil {
		return Record{}, err
	}
	if doc["contract"] != contract || action == "op.result" && doc["kind"] != "op.result" {
		return Record{}, fault(400, "invalid_request")
	}
	id := text(doc["client_event_id"])
	if !uuid(id) {
		return Record{}, fault(400, "invalid_request")
	}
	var prior, document []byte
	err = tx.QueryRow(ctx, `SELECT original_bytes,document FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND client_event_id=$3`, st.Tenant, st.ID, id).Scan(&prior, &document)
	if err == nil {
		if !bytes.Equal(raw, prior) {
			return Record{}, fault(409, "idempotency_conflict")
		}
		return Record{Document: document, Bytes: string(prior)}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, err
	}
	if doc["sid"] != st.ID {
		return Record{}, fault(403, "forbidden")
	}
	if _, has := doc["worker_generation"]; has && number(doc["worker_generation"]) != c.Generation {
		return Record{}, fault(409, "fenced_generation")
	}
	writer := object(doc["writer"])
	if writer["kind"] == "host" {
		return Record{}, fault(403, "forbidden")
	}
	if writer["kind"] == "worker" && number(writer["generation"]) != c.Generation {
		return Record{}, fault(409, "fenced_generation")
	}
	doc, err = s.validator.Validate(raw, contract)
	if err != nil {
		return Record{}, err
	}
	if _, has := doc["seq"]; has {
		return Record{}, fault(400, "invalid_request")
	}
	if contract == "aithema.journal.record" && doc["kind"] == "op.result" && !strings.HasPrefix(text(object(doc["data"])["op_key"]), st.ID+":") {
		return Record{}, fault(400, "invalid_request")
	}
	if contract == "aithema.spec.snapshot" {
		if doc["host_mode"] != st.HostMode || number(doc["consumed_seq"]) > st.Seq || number(doc["consumed_seq"]) < st.ConsumedSeq {
			return Record{}, fault(400, "invalid_request")
		}
		for _, op := range array(doc["pending_ops"]) {
			if !strings.HasPrefix(text(object(op)["op_key"]), st.ID+":") {
				return Record{}, fault(400, "invalid_request")
			}
		}
		if number(doc["expected_prev_rev"]) != st.WorkingRev {
			return Record{}, fault(409, "snapshot_conflict")
		}
	}
	record, err := appendRecord(ctx, tx, st, raw, doc)
	if err != nil {
		return Record{}, err
	}
	if contract == "aithema.spec.snapshot" {
		_, err = tx.Exec(ctx, `UPDATE aithema_sessions SET working_rev=$3,snapshot_seq=$4,consumed_seq=$5 WHERE tenant_id=$1 AND sid=$2`, st.Tenant, st.ID, number(doc["working_rev"]), st.Seq, number(doc["consumed_seq"]))
	}
	return record, err
}
func (s *Store) read(ctx context.Context, tx pgx.Tx, st *session, area, action string, q url.Values) (json.RawMessage, error) {
	if area == "ledger" {
		return s.openHolds(ctx, tx, st, q)
	}
	if action == "authority" {
		if !queryValid(q) {
			return nil, fault(400, "invalid_request")
		}
		return marshal(state(st, s.now())), nil
	}
	stored, err := storedFormat(q)
	if err != nil {
		return nil, err
	}
	if action == "cursor" {
		if !queryValid(q, "format") {
			return nil, fault(400, "invalid_request")
		}
		var snapshot any
		if st.SnapshotSeq != nil {
			var raw, doc []byte
			if err := tx.QueryRow(ctx, `SELECT original_bytes,document FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND seq=$3`, st.Tenant, st.ID, *st.SnapshotSeq).Scan(&raw, &doc); err != nil {
				return nil, err
			}
			snapshot = formatRecord(Record{Document: doc, Bytes: string(raw)}, stored)
		}
		return marshal(map[string]any{"seq": st.Seq, "working_rev": st.WorkingRev, "snapshot": snapshot}), nil
	}
	if !queryValid(q, "format", "after", "ids") {
		return nil, fault(400, "invalid_request")
	}
	after, err := count(q.Get("after"), 0)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	if _, has := q["ids"]; has {
		parts := strings.Split(q.Get("ids"), ",")
		if len(parts) > 1000 || q.Get("ids") == "" {
			return nil, fault(400, "invalid_request")
		}
		for _, part := range parts {
			n, err := count(part, -1)
			if err != nil || n < 0 {
				return nil, fault(400, "invalid_request")
			}
			ids = append(ids, n)
		}
	}
	rows, err := tx.Query(ctx, `SELECT original_bytes,document FROM aithema_journal_records WHERE tenant_id=$1 AND sid=$2 AND (CASE WHEN $3::boolean THEN seq=ANY($4::bigint[]) ELSE seq>$5 END) ORDER BY seq`, st.Tenant, st.ID, q.Has("ids"), ids, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []any{}
	for rows.Next() {
		var raw, doc []byte
		if err := rows.Scan(&raw, &doc); err != nil {
			return nil, err
		}
		records = append(records, formatRecord(Record{Document: doc, Bytes: string(raw)}, stored))
	}
	return marshal(records), rows.Err()
}
