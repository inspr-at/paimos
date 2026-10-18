// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type outboxSend struct {
	ClientID string  `json:"client_message_id"`
	Body     string  `json:"body"`
	ReplyTo  *string `json:"reply_to,omitempty"`
}
type finalSend struct {
	WorkerBindingRequest
	outboxSend
}
type outboxReceipt struct {
	MessageID string `json:"message_id"`
	State     string `json:"state"`
}
type outboxResult struct {
	Contract    string         `json:"contract"`
	Message     historyMessage `json:"message"`
	Receipt     outboxReceipt  `json:"receipt"`
	notify, key string
}

func (o outboxResult) afterCommit(m *Module) {
	if o.notify != "" {
		// A notification may be lost on restart or overload. The committed final
		// message/receipt remains retrievable through the bounded outbox/history.
		_, _ = m.live.publish(o.key, "", "", 0, map[string]any{"type": o.notify, "message_id": o.Message.ID, "receipt": o.Receipt})
	}
}
func validFinal(in outboxSend) error {
	if len(in.Body) > 65536 {
		return workorders.Fail(413, "chat body too large")
	}
	if in.Body == "" || !utf8.ValidString(in.Body) || strings.ContainsRune(in.Body, 0) || len(in.ClientID) < 1 || len(in.ClientID) > 64 || !utf8.ValidString(in.ClientID) || strings.ContainsRune(in.ClientID, 0) || in.ReplyTo != nil && !workorders.UUID(*in.ReplyTo) {
		return workorders.Fail(400, "invalid final chat message")
	}
	return nil
}
func (m *Module) sendOutbox(r *http.Request, tx pgx.Tx, p tenant.Principal, in outboxSend) (any, error) {
	if err := validFinal(in); err != nil {
		return nil, err
	}
	c, err := participant(r, tx, p)
	if err != nil {
		return nil, err
	}
	var project, recipient, session string
	err = tx.QueryRow(r.Context(), `SELECT t.project_id::text,b.agent_principal_id::text,b.session_id::text FROM chat_threads t JOIN chat_session_bindings b ON b.tenant_id=t.tenant_id AND b.role_id=t.role_id WHERE t.id=$1 AND b.valid_to IS NULL FOR NO KEY UPDATE OF t`, c.Thread).Scan(&project, &recipient, &session)
	if err != nil {
		return nil, err
	}
	if err = require(r.Context(), tx, p, "chat.send", project); err != nil {
		return nil, err
	}
	// Pin each input to the chosen session. Handover never resends input to a
	// successor, and ordinary inbox/drain/wake paths cannot see chat rows.
	if _, err = tx.Exec(r.Context(), `SELECT 1 FROM harness_sessions WHERE id=$1 FOR NO KEY UPDATE`, session); err != nil {
		return nil, err
	}
	return storeFinal(r.Context(), tx, p, c.Thread, project, c.Person, recipient, "", session, in)
}
func (m *Module) finalMessage(r *http.Request, tx pgx.Tx, p tenant.Principal, in finalSend) (any, error) {
	if err := validFinal(in.outboxSend); err != nil {
		return nil, err
	}
	if err := accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	s, err := AuthorizeWorkerTx(r.Context(), tx, r, p, in.WorkerBindingRequest)
	if err != nil {
		return nil, err
	}
	if err = require(r.Context(), tx, p, "chat.send", s.ProjectID); err != nil {
		return nil, err
	}
	if err = workerKeyPermissionTx(r.Context(), tx, r, p, "chat.send"); err != nil {
		return nil, err
	}
	return storeFinal(r.Context(), tx, p, in.ConversationID, s.ProjectID, p.ID, s.OwnerPersonID, s.ID, "", in.outboxSend)
}

const outboxColumns = `id::text,sent_event_id,body,reply_to_id::text`

func scanOutbox(row pgx.Row, thread string) (historyMessage, error) {
	o := historyMessage{Conversation: thread, Payload: "inline"}
	var seq int64
	err := row.Scan(&o.ID, &seq, &o.Body, &o.ReplyTo)
	o.Sequence = strconv.FormatInt(seq, 10)
	o.Event = o.Sequence
	return o, err
}
func storeFinal(ctx context.Context, tx pgx.Tx, p tenant.Principal, thread, project, sender, recipient, senderSession, recipientSession string, in outboxSend) (outboxResult, error) {
	o := outboxResult{Contract: "chat-live-v1", key: liveKey(p.TenantID, thread)}
	key := "chat/" + thread + "/" + in.ClientID
	existing, err := scanOutbox(tx.QueryRow(ctx, `SELECT `+outboxColumns+` FROM inbox_messages WHERE chat_thread_id=$1 AND sender_principal_id=$2 AND idempotency_key=$3 FOR NO KEY UPDATE`, thread, sender, key), thread)
	if err == nil {
		if existing.Body != in.Body || (existing.ReplyTo == nil) != (in.ReplyTo == nil) || existing.ReplyTo != nil && *existing.ReplyTo != *in.ReplyTo {
			return o, workorders.Fail(409, "chat client message ID conflicts")
		}
		o.Message = existing
		o.Receipt, err = receiptTx(ctx, tx, thread, existing)
		return o, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return o, err
	}
	if in.ReplyTo != nil {
		var id string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM inbox_messages WHERE chat_thread_id=$1 AND id=$2 FOR NO KEY UPDATE`, thread, *in.ReplyTo).Scan(&id); err != nil {
			return o, err
		}
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return o, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{sender, recipient}); err != nil {
		return o, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR KEY SHARE`, project); err != nil {
		return o, err
	}
	// All FK parents are locked before event-counter acquisition. Only final
	// text enters the inbox; the private event contains identifiers, never text.
	e, err := events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: "chat.live_message", After: map[string]string{"conversation_id": thread, "message_id": id}})
	if err != nil {
		return o, err
	}
	o.Message, err = scanOutbox(tx.QueryRow(ctx, `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,chat_thread_id,sender_session_id,recipient_session_id,sent_event_id,body,idempotency_key,reply_to_id) VALUES($1,$2,$3,$4,$5,nullif($6,'')::uuid,nullif($7,'')::uuid,$8,$9,$10,$11) RETURNING `+outboxColumns, p.TenantID, id, sender, recipient, thread, senderSession, recipientSession, e.ID, in.Body, key, in.ReplyTo), thread)
	o.Receipt = outboxReceipt{id, "sent"}
	o.notify = "message"
	return o, err
}

// Receipt evidence is content-free and independent of the legacy ACK ledger.
// Reading a page, reconnecting and queueing input never imply delivery/read.
func receiptTx(ctx context.Context, tx pgx.Tx, thread string, msg historyMessage) (outboxReceipt, error) {
	o := outboxReceipt{msg.ID, "sent"}
	var recipient string
	var delivered, read bool
	err := tx.QueryRow(ctx, `SELECT recipient_principal_id::text,fetched_at IS NOT NULL,acked_at IS NOT NULL FROM inbox_messages WHERE chat_thread_id=$1 AND id=$2`, thread, msg.ID).Scan(&recipient, &delivered, &read)
	if err != nil {
		return o, err
	}
	if delivered {
		o.State = "delivered"
	}
	if read {
		o.State = "read"
	}
	seq, err := strconv.ParseInt(msg.Sequence, 10, 64)
	if err != nil {
		return o, err
	}
	var bits []byte
	err = tx.QueryRow(ctx, `SELECT bitmap FROM chat_seen_chunks WHERE conversation_id=$1 AND person_id=$2 AND chunk_index=$3`, thread, recipient, seq/4096).Scan(&bits)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return o, err
	}
	if len(bits) == 512 && bits[seq%4096/8]&(1<<uint(seq%8)) != 0 {
		o.State = "read"
	}
	return o, nil
}

type outboxPage struct {
	Contract string         `json:"contract"`
	Items    []outboxResult `json:"items"`
	Next     *string        `json:"next_cursor"`
}

func (m *Module) listOutbox(r *http.Request, tx pgx.Tx, p tenant.Principal, _ struct{}) (any, error) {
	c, err := participant(r, tx, p)
	if err != nil {
		return nil, err
	}
	limit, err := boundedLimit(r, 50, 25)
	if err != nil {
		return nil, err
	}
	c.Kind = "outbox"
	if raw := r.URL.Query().Get("after"); raw != "" {
		c, err = decodeCursor(raw, c)
		if err != nil {
			return nil, err
		}
	}
	return outboxPageTx(r.Context(), tx, c, limit, "", "")
}

type workerPageRequest struct {
	WorkerBindingRequest
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func (m *Module) workerOutbox(r *http.Request, tx pgx.Tx, p tenant.Principal, in workerPageRequest) (any, error) {
	if in.Limit < 0 || in.Limit > 50 {
		return nil, workorders.Fail(400, "invalid chat limit")
	}
	if in.Limit == 0 {
		in.Limit = 25
	}
	if err := accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	s, err := AuthorizeWorkerTx(r.Context(), tx, r, p, in.WorkerBindingRequest)
	if err != nil {
		return nil, err
	}
	c := historyCursor{Tenant: p.TenantID, Person: p.ID, Thread: in.ConversationID, Kind: "worker-outbox/" + s.ID + "/" + in.BindingEpoch}
	if in.After != "" {
		c, err = decodeCursor(in.After, c)
		if err != nil {
			return nil, err
		}
	}
	return outboxPageTx(r.Context(), tx, c, in.Limit, p.ID, s.ID)
}
func outboxPageTx(ctx context.Context, tx pgx.Tx, c historyCursor, limit int, recipient, session string) (outboxPage, error) {
	o := outboxPage{Contract: "chat-live-v1", Items: []outboxResult{}}
	rows, err := tx.Query(ctx, `SELECT `+outboxColumns+` FROM inbox_messages WHERE chat_thread_id=$1 AND sent_event_id>$2 AND ($3='' OR recipient_principal_id=nullif($3,'')::uuid AND recipient_session_id=nullif($4,'')::uuid) ORDER BY sent_event_id LIMIT $5`, c.Thread, c.Position, recipient, session, limit+1)
	if err != nil {
		return o, err
	}
	msgs := []historyMessage{}
	bytes := 1024
	more := false
	for rows.Next() {
		msg, err := scanOutbox(rows, c.Thread)
		if err != nil {
			rows.Close()
			return o, err
		}
		// Escaped JSON can cost six bytes per source byte. Bound before building
		// the page and leave room for receipt metadata and opaque cursors.
		cost := 6*len(msg.Body) + 2048
		if len(msgs) == limit || bytes+cost > 1<<20 {
			more = true
			break
		}
		msgs = append(msgs, msg)
		bytes += cost
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return o, err
	}
	for _, msg := range msgs {
		receipt, err := receiptTx(ctx, tx, c.Thread, msg)
		if err != nil {
			return o, err
		}
		o.Items = append(o.Items, outboxResult{Contract: "chat-live-v1", Message: msg, Receipt: receipt})
	}
	if more && len(msgs) > 0 {
		c.Position, _ = strconv.ParseInt(msgs[len(msgs)-1].Sequence, 10, 64)
		token := encodeCursor(c)
		o.Next = &token
	}
	return o, nil
}

type workerReceiptRequest struct {
	WorkerBindingRequest
	MessageID string `json:"message_id"`
	State     string `json:"state"`
}

func (m *Module) workerReceipt(r *http.Request, tx pgx.Tx, p tenant.Principal, in workerReceiptRequest) (any, error) {
	if !workorders.UUID(in.MessageID) || in.State != "delivered" && in.State != "read" {
		return nil, workorders.Fail(400, "invalid chat receipt")
	}
	if err := accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	s, err := AuthorizeWorkerTx(r.Context(), tx, r, p, in.WorkerBindingRequest)
	if err != nil {
		return nil, err
	}
	msg, err := scanOutbox(tx.QueryRow(r.Context(), `SELECT `+outboxColumns+` FROM inbox_messages WHERE chat_thread_id=$1 AND id=$2 AND recipient_principal_id=$3 AND recipient_session_id=$4 FOR NO KEY UPDATE`, in.ConversationID, in.MessageID, p.ID, s.ID), in.ConversationID)
	if err != nil {
		return nil, err
	}
	receipt, err := receiptTx(r.Context(), tx, in.ConversationID, msg)
	if err != nil {
		return nil, err
	}
	o := outboxResult{Contract: "chat-live-v1", Message: msg, Receipt: receipt, key: liveKey(p.TenantID, in.ConversationID)}
	if receipt.State == "read" || receipt.State == in.State {
		return o, nil
	}
	if in.State == "read" && receipt.State != "delivered" {
		return nil, workorders.Fail(409, "chat receipt requires delivery evidence")
	}
	if _, err = tx.Exec(r.Context(), `UPDATE inbox_messages SET fetched_at=coalesce(fetched_at,clock_timestamp()),acked_at=CASE WHEN $3='read' THEN coalesce(acked_at,clock_timestamp()) ELSE acked_at END,acked_by_principal_id=CASE WHEN $3='read' THEN $4::uuid ELSE acked_by_principal_id END WHERE chat_thread_id=$1 AND id=$2`, in.ConversationID, in.MessageID, in.State, p.ID); err != nil {
		return nil, err
	}
	_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: &s.ProjectID, Type: "chat.live_receipt", After: map[string]string{"conversation_id": in.ConversationID, "message_id": in.MessageID, "state": in.State}})
	o.Receipt.State = in.State
	o.notify = "receipt"
	return o, err
}
