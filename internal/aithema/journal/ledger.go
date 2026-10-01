// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ledger(ctx context.Context, tx pgx.Tx, st *session, c tokens.Claims, action string, raw []byte) (Result, error) {
	doc, err := decode(raw)
	if err != nil {
		return Result{}, err
	}
	if doc["contract"] != "aithema.budget.message" || doc["type"] != action+"_request" || object(doc["body"]) == nil {
		return Result{}, fault(400, "invalid_request")
	}
	body := object(doc["body"])
	if sid, ok := body["sid"]; ok && sid != st.ID {
		return Result{}, fault(403, "forbidden")
	}
	if epoch, ok := integer(body["auth_epoch"]); ok && epoch != st.Epoch {
		return Result{}, fault(409, "revoked")
	}
	if action == "admit" {
		var previous, verdict []byte
		var state string
		err := tx.QueryRow(ctx, `SELECT original_bytes,verdict,state FROM aithema_budget_holds WHERE tenant_id=$1 AND sid=$2 AND attempt_id=$3`, st.Tenant, st.ID, text(body["attempt_id"])).Scan(&previous, &verdict, &state)
		if err == nil {
			if !bytes.Equal(previous, raw) {
				return Result{}, fault(409, "idempotency_conflict")
			}
			status := 200
			if state == "denied" {
				status = 402
			}
			return Result{Status: status, Body: verdict}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Result{}, err
		}
	}
	if gen, ok := integer(body["worker_generation"]); ok && gen != c.Generation {
		return Result{}, fault(409, "fenced_generation")
	}
	doc, err = s.validator.Validate(raw, "aithema.budget.message")
	if err != nil {
		return Result{}, err
	}
	body = object(doc["body"])
	switch action {
	case "admit":
		return s.admit(ctx, tx, st, c, body, raw)
	case "claim":
		h, err := getHold(ctx, tx, st, text(body["hold_id"]), "")
		if err != nil {
			return Result{}, err
		}
		if h.State == "closed" {
			return Result{}, claimFault("hold_closed")
		}
		if h.Claim != "" {
			return Result{}, claimFault("already_claimed")
		}
		if h.Epoch != st.Epoch {
			return Result{}, fault(409, "revoked")
		}
		if h.Generation != st.Generation {
			return Result{}, fault(409, "fenced_generation")
		}
		id := newID()
		_, err = tx.Exec(ctx, `INSERT INTO aithema_budget_claims(tenant_id,sid,claim_id,hold_id,request_sha256,worker_generation,auth_epoch,state,claimed_at) VALUES($1,$2,$3,$4,$5,$6,$7,'claimed',$8)`, st.Tenant, st.ID, id, h.ID, text(body["request_sha256"]), st.Generation, st.Epoch, s.now())
		return Result{Status: 200, Body: budget("claim_response", map[string]any{"claim_id": id})}, err
	case "recover":
		h, err := getHold(ctx, tx, st, text(body["hold_id"]), "")
		if err != nil {
			return Result{}, err
		}
		if h.State == "closed" {
			return Result{Status: 200, Body: h.response()}, nil
		}
		reason, charged := "void", int64(0)
		if h.Claim != "" {
			reason, charged = "unknown", h.Max
		}
		return s.close(ctx, tx, st, h, reason, charged, nil)
	case "settle":
		h, err := getHold(ctx, tx, st, "", text(body["claim_id"]))
		if err != nil {
			return Result{}, err
		}
		if h.Settlement != nil {
			if !bytes.Equal(raw, h.Settlement) {
				return Result{}, fault(409, "idempotency_conflict")
			}
			return Result{Status: 200, Body: h.response()}, nil
		}
		if h.State == "closed" {
			return Result{}, fault(409, "hold_closed")
		}
		reason, charged := text(body["outcome"]), number(body["actual_micro"])
		if reason == "unknown" {
			charged = h.Max
		}
		if charged > h.Max {
			return Result{}, fault(400, "invalid_request")
		}
		// A current worker can recover an older claim; a stale worker cannot settle
		// it. Work dispatched before takeover is conservatively charged at maximum.
		if h.Generation != st.Generation || h.Epoch != st.Epoch {
			reason, charged = "unknown", h.Max
		}
		return s.close(ctx, tx, st, h, reason, charged, raw)
	}
	return Result{}, fault(404, "not_found")
}
func claimFault(code string) *Fault {
	e := fault(409, code)
	e.Document = budget("claim_response", map[string]any{"error": code})
	return e
}

type hold struct {
	ID, Claim, State, Reason, LaneKind string
	Generation, Epoch, Max, Charged    int64
	Settlement                         []byte
}

func getHold(ctx context.Context, tx pgx.Tx, st *session, id, claim string) (*hold, error) {
	h := &hold{}
	var err error
	if id != "" && !uuid(id) || claim != "" && !uuid(claim) {
		return nil, fault(400, "invalid_request")
	}
	err = tx.QueryRow(ctx, `SELECT h.hold_id::text,COALESCE(c.claim_id::text,''),h.state,COALESCE(h.closed_reason,''),h.lane_kind,h.worker_generation,h.auth_epoch,h.max_micro,COALESCE(h.charged_micro,0),h.settlement_bytes FROM aithema_budget_holds h LEFT JOIN aithema_budget_claims c USING(tenant_id,sid,hold_id) WHERE h.tenant_id=$1 AND h.sid=$2 AND h.state<>'denied' AND (CASE WHEN $3::text<>'' THEN h.hold_id::text=$3 ELSE c.claim_id::text=$4 END)`, st.Tenant, st.ID, id, claim).Scan(&h.ID, &h.Claim, &h.State, &h.Reason, &h.LaneKind, &h.Generation, &h.Epoch, &h.Max, &h.Charged, &h.Settlement)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fault(404, "not_found")
	}
	return h, err
}
func (h *hold) response() json.RawMessage {
	body := map[string]any{"hold_id": h.ID, "closed_reason": h.Reason, "charged_micro": h.Charged}
	if h.LaneKind == "operator_local" {
		body["lane_kind"] = h.LaneKind
	}
	return budget("recover_response", body)
}
func (s *Store) close(ctx context.Context, tx pgx.Tx, st *session, h *hold, reason string, charged int64, settlement []byte) (Result, error) {
	_, err := tx.Exec(ctx, `UPDATE aithema_budget_holds SET state='closed',closed_reason=$4,charged_micro=$5,closed_at=$6,settlement_bytes=$7 WHERE tenant_id=$1 AND sid=$2 AND hold_id=$3`, st.Tenant, st.ID, h.ID, reason, charged, s.now(), settlement)
	if err != nil {
		return Result{}, err
	}
	if h.Claim != "" {
		_, err = tx.Exec(ctx, `UPDATE aithema_budget_claims SET state=$4,settled_micro=$5,settled_at=$6 WHERE tenant_id=$1 AND sid=$2 AND claim_id=$3`, st.Tenant, st.ID, h.Claim, reason, charged, s.now())
		if err != nil {
			return Result{}, err
		}
	}
	h.State, h.Reason, h.Charged = "closed", reason, charged
	return Result{Status: 200, Body: h.response()}, nil
}
func (s *Store) admit(ctx context.Context, tx pgx.Tx, st *session, c tokens.Claims, body map[string]any, raw []byte) (Result, error) {
	if body["currency"] != st.Currency {
		return Result{}, fault(400, "invalid_request")
	}
	laneKind := text(body["lane_kind"])
	if laneKind == "" {
		laneKind = "remote"
	}
	if laneKind == "operator_local" && !in(text(body["lane"]), st.LocalLanes...) {
		return Result{}, fault(403, "forbidden")
	}
	var principalCap, tenantCap int64
	if err := tx.QueryRow(ctx, `SELECT principal_day_cap,tenant_day_cap FROM aithema_budget_policy WHERE tenant_id=$1 AND currency=$2`, st.Tenant, st.Currency).Scan(&principalCap, &tenantCap); err != nil {
		return Result{}, err
	}
	day := s.now().Format("2006-01-02")
	// SUM(bigint) is PostgreSQL numeric. Keep it exact even if policy has been
	// lowered after substantial historical usage; never overflow int64 in Go.
	var sessionUsage, principalUsage, tenantUsage string
	err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN sid=$2 THEN CASE WHEN state='closed' THEN charged_micro ELSE max_micro END ELSE 0 END),0)::text, COALESCE(SUM(CASE WHEN principal_id=$3 AND admission_day=$4 THEN CASE WHEN state='closed' THEN charged_micro ELSE max_micro END ELSE 0 END),0)::text, COALESCE(SUM(CASE WHEN admission_day=$4 THEN CASE WHEN state='closed' THEN charged_micro ELSE max_micro END ELSE 0 END),0)::text FROM aithema_budget_holds WHERE tenant_id=$1 AND currency=$5 AND state<>'denied'`, st.Tenant, st.ID, c.Actor.Subject, day, st.Currency).Scan(&sessionUsage, &principalUsage, &tenantUsage)
	if err != nil {
		return Result{}, err
	}
	denied := ""
	if !st.Evidence {
		denied = "no_evidence"
	}
	remaining := big.NewInt(tokens.MaxSafeInteger)
	maximum := number(body["max_micro"])
	for i, usage := range []string{sessionUsage, principalUsage, tenantUsage} {
		used, ok := new(big.Int).SetString(usage, 10)
		if !ok {
			return Result{}, fault(503, "unavailable")
		}
		available := new(big.Int).Sub(big.NewInt([]int64{st.SessionCap, principalCap, tenantCap}[i]), used)
		if denied == "" && available.Cmp(big.NewInt(maximum)) < 0 {
			denied = []string{"session_cap", "principal_day_cap", "tenant_day_cap"}[i]
		}
		if available.Cmp(remaining) < 0 {
			remaining = available
		}
	}
	status, state := 200, "admitted"
	var verdict []byte
	var deniedValue any
	id := newID()
	if denied != "" {
		status, state, deniedValue = 402, "denied", denied
		document := budget("admit_response", map[string]any{"denied": denied})
		verdict = marshal(map[string]any{"error": "budget_denied", "code": "budget_denied", "document": json.RawMessage(document)})
	} else {
		remaining.Sub(remaining, big.NewInt(maximum))
		verdict = budget("admit_response", map[string]any{"hold_id": id, "remaining_micro": remaining.Int64()})
	}
	_, err = tx.Exec(ctx, `INSERT INTO aithema_budget_holds(tenant_id,sid,attempt_id,hold_id,worker_generation,auth_epoch,principal_id,admission_day,lane,lane_kind,max_micro,currency,state,denied_reason,original_bytes,verdict,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, st.Tenant, st.ID, text(body["attempt_id"]), id, st.Generation, st.Epoch, c.Actor.Subject, day, text(body["lane"]), laneKind, maximum, st.Currency, state, deniedValue, raw, verdict, s.now())
	return Result{Status: status, Body: verdict}, err
}
func (s *Store) openHolds(ctx context.Context, tx pgx.Tx, st *session, q url.Values) (json.RawMessage, error) {
	if !queryValid(q, "state", "limit", "cursor") || q.Get("state") != "open" {
		return nil, fault(400, "invalid_request")
	}
	limit, err := count(q.Get("limit"), 1000)
	if err != nil || limit < 1 || limit > 1000 {
		return nil, fault(400, "invalid_request")
	}
	var cursor int64
	if value, has := q["cursor"]; has {
		parts := strings.Split(value[0], ":")
		if len(parts) != 2 || parts[0] != st.ID || parts[1] == "" {
			return nil, fault(400, "invalid_request")
		}
		cursor, err = count(parts[1], 0)
		if err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT h.hold_id::text,h.attempt_id,c.claim_id IS NOT NULL,h.hold_order FROM aithema_budget_holds h LEFT JOIN aithema_budget_claims c USING(tenant_id,sid,hold_id) WHERE h.tenant_id=$1 AND h.sid=$2 AND h.state='admitted' AND h.hold_order>$3 ORDER BY h.hold_order LIMIT $4`, st.Tenant, st.ID, cursor, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	holds := []any{}
	var next any
	var last int64
	for rows.Next() {
		var id, attempt string
		var claimed bool
		var order int64
		if err := rows.Scan(&id, &attempt, &claimed, &order); err != nil {
			return nil, err
		}
		if int64(len(holds)) == limit {
			next = st.ID + ":" + strconv.FormatInt(last, 10)
			break
		}
		holds = append(holds, map[string]any{"hold_id": id, "attempt_id": attempt, "claimed": claimed})
		last = order
	}
	return budget("holds_list", map[string]any{"sid": st.ID, "state": "open", "holds": holds, "next_cursor": next}), rows.Err()
}
