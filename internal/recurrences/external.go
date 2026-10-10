// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type ExternalEvent struct {
	DeliveryID string    `json:"delivery_id"`
	Event      string    `json:"event"`
	OccurredAt time.Time `json:"occurred_at"`
	Source     string    `json:"source"`
	Ref        string    `json:"ref"`
}

type externalReceipt struct {
	EventID   int64 `json:"event_id"`
	Duplicate bool  `json:"duplicate"`
}

var deliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func externalKey(sender, delivery string) string {
	digest := sha256.Sum256([]byte(sender + "\n" + delivery))
	return "external:" + hex.EncodeToString(digest[:])
}

func signatureMessage(tenantID, recurrenceID string, raw []byte) []byte {
	return append([]byte("aeon-recurrence-event-v1\n"+tenantID+"\n"+recurrenceID+"\n"), raw...)
}

func (m *Module) receiveExternal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent {
		httpError(w, 403, "external sender must be an agent")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil || len(raw) == 0 {
		httpError(w, 400, "external event must be at most 8192 bytes")
		return
	}
	var in ExternalEvent
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil || d.Decode(new(any)) != io.EOF || !deliveryIDPattern.MatchString(in.DeliveryID) ||
		(in.Event != "external.tag" && in.Event != "external.deploy") || in.OccurredAt.IsZero() ||
		strings.TrimSpace(in.Source) == "" || len(in.Source) > 256 || strings.TrimSpace(in.Ref) == "" || len(in.Ref) > 256 {
		httpError(w, 400, "invalid external event")
		return
	}
	encodedSignature := r.Header.Get("X-Aeon-Signature")
	if len(encodedSignature) != 88 {
		httpError(w, 401, "invalid external event signature")
		return
	}
	signature, err := base64.StdEncoding.DecodeString(encodedSignature)
	if err != nil || len(signature) != ed25519.SignatureSize || len(r.Header.Get("X-Aeon-Signature")) != 88 {
		httpError(w, 401, "invalid external event signature")
		return
	}
	var out externalReceipt
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := lock(ctx, tx, p.TenantID, false); err != nil {
			return err
		}
		item, err := load(ctx, tx, strings.ToLower(r.PathValue("recurrenceId")), false)
		if err != nil {
			return err
		}
		if err = manage(ctx, tx, p, item.ProjectID); err != nil {
			return err
		}
		if err = authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: item.ProjectID}); err != nil {
			return err
		}
		sender := item.Trigger.External
		if item.Trigger.Kind != "event" || sender == nil || sender.PrincipalID != p.ID || item.Trigger.Event != in.Event {
			return workorders.Fail(403, "event does not belong to this sender or recurrence")
		}
		key, err := base64.StdEncoding.DecodeString(sender.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, signatureMessage(p.TenantID, item.ID, raw), signature) {
			return workorders.Fail(401, "invalid external event signature")
		}
		now, err := m.clock(ctx, tx)
		if err != nil {
			return err
		}
		if in.OccurredAt.Before(now.Add(-5*time.Minute)) || in.OccurredAt.After(now.Add(5*time.Minute)) {
			return workorders.Fail(401, "external event timestamp outside the five-minute window")
		}
		identity := externalKey(p.ID, in.DeliveryID)
		digest := sha256.Sum256(raw)
		hash := hex.EncodeToString(digest[:])
		var priorHash string
		err = tx.QueryRow(ctx, `SELECT id,metadata->>'payload_sha256' FROM events WHERE node_id=$1 AND type='recurrence.external_received' AND metadata->>'recurrence_id'=$2 AND metadata->>'receipt_key'=$3 ORDER BY id LIMIT 1`, item.ProjectID, item.ID, identity).Scan(&out.EventID, &priorHash)
		if err == nil {
			if priorHash != hash {
				return workorders.Fail(409, "delivery_id already belongs to another payload")
			}
			out.Duplicate = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if item.Paused {
			return workorders.Fail(409, "recurrence is paused")
		}
		// Reference visibility also applies to our own events. Reject a payload
		// naming a hidden node before recording it, so later receipt reads cannot
		// miss the delivery and accidentally accept a replay as a new event.
		payload, _ := json.Marshal(in)
		var visible bool
		if err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM unnest(aeon_event_node_refs($1::uuid,'recurrence.external_received',NULL,$2::jsonb)) ref(id) WHERE NOT EXISTS(SELECT 1 FROM nodes n WHERE n.id=ref.id))`, p.TenantID, payload).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return workorders.Fail(404, "external source reference unavailable")
		}
		// Lock the target rows and recurrence before the event counter. Intake
		// creates neither occurrence tickets nor agent runs.
		if err = validateTarget(ctx, tx, item.Input, true); err != nil {
			return err
		}
		if _, err = load(ctx, tx, item.ID, true); err != nil {
			return err
		}
		metadata, _ := json.Marshal(map[string]string{"recurrence_id": item.ID, "receipt_key": identity, "payload_sha256": hash})
		e, err := events.Append(ctx, tx, p, events.Change{NodeID: &item.ProjectID, Type: "recurrence.external_received", After: in, Metadata: metadata, At: &now})
		out.EventID = e.ID
		return err
	})
	reply(w, 202, out, err)
}
