// SPDX-License-Identifier: AGPL-3.0-only

package receipts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type cursor struct {
	Scope string `json:"s"`
	After string `json:"a"`
	Until int64  `json:"u"`
}

func receiptScope(actor nativecontract.Actor, binding nativecontract.Binding) string {
	raw, _ := json.Marshal(struct {
		Caller  string
		Binding nativecontract.Binding
	}{actor.PrincipalID, binding})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Repository) Receipts(ctx context.Context, actor nativecontract.Actor, binding nativecontract.Binding, q nativecontract.PageQuery) (nativecontract.ReceiptPage, error) {
	out := nativecontract.ReceiptPage{Items: []nativecontract.Receipt{}}
	if q.Limit == 0 {
		q.Limit = 25
	}
	if q.Limit < 1 || q.Limit > 100 || len(q.After) > 512 {
		return out, nativereceipts.Fail(nativecontract.ReasonInvalidInput)
	}
	if err := nativereceipts.Validate(s.validator, "NativeBinding", binding); err != nil {
		return out, err
	}
	p, err := principal(ctx, actor)
	if err != nil {
		return out, err
	}
	if p.TenantID != binding.TenantID {
		return out, nativereceipts.Fail(nativecontract.ReasonWrongInstance)
	}
	scope := receiptScope(actor, binding)
	after := ""
	if q.After != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.After)
		if err != nil {
			return out, nativereceipts.Fail(nativecontract.ReasonCursorExpired)
		}
		var c cursor
		if json.Unmarshal(raw, &c) != nil || c.Scope != scope || !workorders.UUID(c.After) || s.now().Unix() >= c.Until || c.Until > s.now().Add(5*time.Minute).Unix() {
			return out, nativereceipts.Fail(nativecontract.ReasonCursorExpired)
		}
		after = c.After
	}
	err = s.write(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.authority.ReadTx(ctx, tx, actor, binding); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: binding.ProjectID}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT receipt FROM native_session_receipts WHERE tenant_id=$1 AND owned_session_id=$2 AND ($3='' OR operation_id>nullif($3,'')::uuid) AND receipt->'binding'->>'runtime_epoch'=$4 ORDER BY operation_id LIMIT $5`, p.TenantID, binding.OwnedSessionID, after, binding.RuntimeEpoch, q.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		bytes := 1024
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if len(out.Items) == q.Limit || bytes+len(raw) > 1<<20 {
				out.Truncated = true
				break
			}
			var item nativecontract.Receipt
			if err := s.validator.DecodeJSON("NativeReceipt", raw, &item); err != nil {
				return err
			}
			if item.Binding == nil || !item.Binding.Equal(binding) {
				return nativereceipts.Fail(nativecontract.ReasonWrongGeneration)
			}
			bytes += len(raw)
			out.Items = append(out.Items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if out.Truncated && len(out.Items) > 0 {
			raw, _ := json.Marshal(cursor{Scope: scope, After: out.Items[len(out.Items)-1].OperationID, Until: s.now().Add(5 * time.Minute).Unix()})
			next := base64.RawURLEncoding.EncodeToString(raw)
			out.NextCursor = &next
		}
		return nil
	})
	return out, err
}
