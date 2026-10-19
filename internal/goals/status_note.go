// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The workspace status note (AEON-1494) is one paragraph per workspace that
// agents write through the goals write API and Goals shows above Waiting on
// you. The latest write wins; goal.status_note_changed events keep its history.
// Reads and writes need nodes.read / nodes.write in the workspace itself.
const (
	MaxStatusNoteRunes  = 2000
	maxStatusNoteBody   = 16 << 10
	StatusNoteEventType = "goal.status_note_changed"
)

type StatusNote struct {
	Text   string    `json:"text"`
	By     string    `json:"by"`
	ByName string    `json:"by_name"`
	SetAt  time.Time `json:"set_at"`
}

type StatusNoteResult struct {
	Note *StatusNote `json:"note"`
	Watermark
}

type StatusNoteWriteResult struct {
	Changed bool        `json:"changed"`
	Note    *StatusNote `json:"note"`
	Watermark
}

// StatusNoteInput is the PATCH body: a text sets the note, null clears it.
type StatusNoteInput struct {
	Text Field[string] `json:"text,omitzero"`
}

func (in StatusNoteInput) Validate() error {
	if !in.Text.Set {
		return invalid("text", "is required; null clears the note")
	}
	if in.Text.Value == nil {
		return nil
	}
	text := strings.TrimSpace(*in.Text.Value)
	if text == "" || utf8.RuneCountInString(text) > MaxStatusNoteRunes {
		return invalid("text", "must contain 1–2000 characters, or null to clear")
	}
	return nil
}

// DecodeStatusNote bounds and strictly decodes a status-note PATCH body.
func DecodeStatusNote(raw []byte) (StatusNoteInput, error) {
	var in StatusNoteInput
	if len(raw) > maxStatusNoteBody {
		return in, invalid("body", "exceeds 16 KiB")
	}
	if !utf8.Valid(raw) {
		return in, invalid("body", "must be UTF-8 JSON")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return in, invalid("body", "must be a JSON object")
	}
	if err := uniqueObjectKeys(raw); err != nil {
		return in, err
	}
	if err := decodeStrict(raw, &in); err != nil {
		return in, invalid("body", err.Error())
	}
	return in, in.Validate()
}

// readStatusNote returns nil when the workspace has no note. The caller has
// already required workspace nodes.read in this transaction.
func readStatusNote(ctx context.Context, tx pgx.Tx, tenantID string) (*StatusNote, error) {
	var note StatusNote
	err := tx.QueryRow(ctx, `SELECT s.body,s.set_by::text,coalesce(p.name,''),s.set_at FROM goal_status_notes s
 LEFT JOIN principals p ON p.tenant_id=s.tenant_id AND p.id=s.set_by WHERE s.tenant_id=$1`, tenantID).Scan(&note.Text, &note.By, &note.ByName, &note.SetAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	note.SetAt = note.SetAt.UTC()
	return &note, nil
}

// visibleStatusNote is the list's view: only a workspace reader sees the note.
func visibleStatusNote(ctx context.Context, tx pgx.Tx, p tenant.Principal) (*StatusNote, error) {
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return nil, nil
		}
		return nil, err
	}
	return readStatusNote(ctx, tx, p.TenantID)
}

func (m *module) statusNote(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	var out StatusNoteResult
	err := m.readSnapshot(r, p, func(ctx context.Context, tx pgx.Tx, w Watermark, _ time.Time) error {
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{}); err != nil {
			return err
		}
		var err error
		out.Note, err = readStatusNote(ctx, tx, p.TenantID)
		out.Watermark = w
		return err
	})
	respond(w, out, err)
}

func (m *module) setStatusNote(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	key, err := CleanIdempotencyKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	raw, err := httpapi.ReadRequestBody(w, r, maxStatusNoteBody, 10*time.Second)
	if err != nil {
		respond(w, nil, err)
		return
	}
	in, err := DecodeStatusNote(raw)
	if err != nil {
		respond(w, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	now := m.now()
	var out StatusNoteWriteResult
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var e error
		out, e = setStatusNoteTx(ctx, tx, p, key, in, now)
		return e
	})
	respond(w, out, err)
}

func statusNoteSnapshot(note *StatusNote) any {
	if note == nil {
		return nil
	}
	return map[string]any{"text": note.Text, "by": note.By, "set_at": note.SetAt.Format(time.RFC3339Nano)}
}

// setStatusNoteTx authorizes under the tenant and tree fences, replays an
// existing receipt, locks the workspace's note row and appends the event last.
// The receipt target is the tenant id: the workspace has no node of its own.
func setStatusNoteTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, key string, in StatusNoteInput, now time.Time) (StatusNoteWriteResult, error) {
	var out StatusNoteWriteResult
	if err := lockWrite(ctx, tx, p); err != nil {
		return out, err
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{}); err != nil {
		return out, err
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	hash := sha256.Sum256(canonical)
	r, err := loadReceipt(ctx, tx, p, key)
	if err != nil {
		return out, err
	}
	if r != nil {
		if r.Method != http.MethodPatch || r.Target != p.TenantID || !slices.Equal(r.Hash, hash[:]) {
			return out, fail(409, "Idempotency-Key: method, target or body conflicts")
		}
		err = json.Unmarshal(r.Response, &out)
		return out, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM goal_status_notes WHERE tenant_id=$1 FOR UPDATE`, p.TenantID); err != nil {
		return out, err
	}
	before, err := readStatusNote(ctx, tx, p.TenantID)
	if err != nil {
		return out, err
	}
	after := before
	switch {
	case in.Text.Value == nil:
		if before != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM goal_status_notes WHERE tenant_id=$1`, p.TenantID); err != nil {
				return out, err
			}
			after = nil
		}
	case before == nil || before.Text != strings.TrimSpace(*in.Text.Value):
		// Repeating the current text is a no-op; attribution changes only when the text does.
		text := strings.TrimSpace(*in.Text.Value)
		if _, err := tx.Exec(ctx, `INSERT INTO goal_status_notes(tenant_id,body,set_by,set_at) VALUES($1,$2,$3,$4)
 ON CONFLICT (tenant_id) DO UPDATE SET body=EXCLUDED.body,set_by=EXCLUDED.set_by,set_at=EXCLUDED.set_at`, p.TenantID, text, p.ID, now.UTC()); err != nil {
			return out, err
		}
		if after, err = readStatusNote(ctx, tx, p.TenantID); err != nil {
			return out, err
		}
	}
	out.Changed = after != before
	out.Note = after
	if out.Changed {
		if _, err := events.Append(ctx, tx, p, events.Change{Type: StatusNoteEventType, Before: statusNoteSnapshot(before), After: statusNoteSnapshot(after)}); err != nil {
			return out, err
		}
	}
	if out.Watermark, err = watermark(ctx, tx); err != nil {
		return out, err
	}
	response, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO goal_update_receipts(tenant_id,actor_principal_id,idempotency_key,method,target_node_id,request_sha256,response,event_id) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8)`, p.TenantID, p.ID, key, http.MethodPatch, p.TenantID, hash[:], response, out.ReadThrough)
	return out, err
}
